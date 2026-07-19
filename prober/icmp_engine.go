// Copyright 2026 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package prober

import (
	"context"
	"log/slog"
	"net/netip"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/randomizedcoder/icmpengine"

	"github.com/prometheus/blackbox_exporter/config"
)

// engineKey identifies a pooled engine by the engine-level settings that vary
// per module. icmpengine sets TTL, source binding and Don't-Fragment at the
// socket level, so one engine is needed per distinct combination.
type engineKey struct {
	ttl          int
	source       netip.Addr
	dontFragment bool
}

// icmpEnginePool holds long-lived icmpengine.Engine instances keyed by
// engineKey. Engines are created lazily on first use and reused for the process
// lifetime; the number of distinct combinations is tiny in practice. There is no
// teardown hook — engines and their two non-privileged sockets live until the
// process exits.
type icmpEnginePool struct {
	mu      sync.Mutex
	engines map[engineKey]*icmpengine.Engine

	// addrLocks serializes concurrent pings to the same (engineKey, addr):
	// icmpengine allows only one in-flight Ping per address per engine.
	addrLocks sync.Map
}

type addrLockKey struct {
	engine engineKey
	addr   netip.Addr
}

var icmpEngines = &icmpEnginePool{engines: make(map[engineKey]*icmpengine.Engine)}

// get returns the engine for the given key, creating and starting it on first
// use. The caller passes an already-validated module TTL (0..255).
func (p *icmpEnginePool) get(key engineKey) (*icmpengine.Engine, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if eng, ok := p.engines[key]; ok {
		return eng, nil
	}
	opts := []icmpengine.Option{
		icmpengine.WithLogger(nil), // keep engine logging decoupled from per-scrape loggers
		icmpengine.WithTTL(key.ttl),
		icmpengine.WithDontFragment(key.dontFragment),
		icmpengine.WithTimeout(time.Second),      // per-ping default; overridden per probe via PingTimeout
		icmpengine.WithReadDeadline(time.Second), // bounds receiver shutdown responsiveness only
	}
	if key.source.IsValid() {
		opts = append(opts, icmpengine.WithSource(key.source))
	}
	eng, err := icmpengine.New(opts...)
	if err != nil {
		return nil, err
	}
	if err := eng.Start(context.Background()); err != nil {
		return nil, err
	}
	p.engines[key] = eng
	return eng, nil
}

// lockAddr returns the mutex serializing pings to the same (engineKey, addr),
// turning a rare same-target collision into a short queue instead of
// ErrDuplicatePing.
func (p *icmpEnginePool) lockAddr(key engineKey, addr netip.Addr) *sync.Mutex {
	m, _ := p.addrLocks.LoadOrStore(addrLockKey{engine: key, addr: addr}, &sync.Mutex{})
	return m.(*sync.Mutex)
}

// probeICMPEngine is the "icmpengine" ICMP backend. It sends a single echo
// request over non-privileged IPPROTO_ICMP sockets, requiring no CAP_NET_RAW.
// It supports payload_size, ttl, source_ip_address and dont_fragment, and emits
// the same probe_icmp_duration_seconds phases as the native prober. It does not
// emit probe_icmp_reply_hop_limit (icmpengine's Result does not expose the reply
// hop limit). dont_fragment is Linux-only; on other platforms the engine fails
// to start and the probe fails with a clear error.
func probeICMPEngine(ctx context.Context, target string, module config.Module, registry *prometheus.Registry, logger *slog.Logger) (success bool) {
	durationGaugeVec := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "probe_icmp_duration_seconds",
		Help: "Duration of icmp request by phase",
	}, []string{"phase"})
	for _, lv := range []string{"resolve", "setup", "rtt"} {
		durationGaugeVec.WithLabelValues(lv)
	}
	registry.MustRegister(durationGaugeVec)

	var source netip.Addr
	if module.ICMP.SourceIPAddress != "" {
		var err error
		source, err = netip.ParseAddr(module.ICMP.SourceIPAddress)
		if err != nil {
			logger.Error("Error parsing source ip address", "srcIP", module.ICMP.SourceIPAddress, "err", err)
			return false
		}
		source = source.Unmap()
	}

	dstIPAddr, lookupTime, err := chooseProtocol(ctx, module.ICMP.IPProtocol, module.ICMP.IPProtocolFallback, target, registry, logger)
	if err != nil {
		logger.Error("Error resolving address", "err", err)
		return false
	}
	durationGaugeVec.WithLabelValues("resolve").Add(lookupTime)

	addr, ok := netip.AddrFromSlice(dstIPAddr.IP)
	if !ok {
		logger.Error("Error converting resolved address", "ip", dstIPAddr.IP)
		return false
	}
	addr = addr.Unmap()

	key := engineKey{ttl: module.ICMP.TTL, source: source, dontFragment: module.ICMP.DontFragment}

	setupStart := time.Now()
	eng, err := icmpEngines.get(key)
	if err != nil {
		logger.Error("Error creating icmpengine (non-privileged ICMP may require net.ipv4.ping_group_range)", "err", err)
		return false
	}

	var opts []icmpengine.PingOption
	if deadline, hasDeadline := ctx.Deadline(); hasDeadline {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			logger.Warn("Context already past deadline before ping")
			return false
		}
		opts = append(opts, icmpengine.PingTimeout(remaining))
	}
	if module.ICMP.PayloadSize > 0 {
		opts = append(opts, icmpengine.PayloadSize(module.ICMP.PayloadSize))
	}
	durationGaugeVec.WithLabelValues("setup").Add(time.Since(setupStart).Seconds())

	// Serialize concurrent probes to the same (engineKey, addr).
	al := icmpEngines.lockAddr(key, addr)
	al.Lock()
	defer al.Unlock()

	logger.Debug("Sending ICMP echo via icmpengine", "addr", addr, "ttl", module.ICMP.TTL, "source", module.ICMP.SourceIPAddress, "dont_fragment", module.ICMP.DontFragment)
	res, err := eng.Ping(ctx, addr, 1, 0, opts...)
	if err != nil {
		logger.Error("Error sending ICMP echo via icmpengine", "err", err)
		return false
	}
	if res.Successes != 1 || len(res.RTTs) == 0 {
		logger.Debug("No ICMP reply received", "successes", res.Successes, "failures", res.Failures)
		return false
	}
	durationGaugeVec.WithLabelValues("rtt").Add(res.RTTs[0].Seconds())
	logger.Debug("Found matching reply packet")
	return true
}
