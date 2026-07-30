package errorpages

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

const (
	// ensureInitialBackoff is the pause after the first failed attempt. It is
	// short because the overwhelmingly likely failure at start-up — CRDs not
	// established yet, API server still warming — clears in seconds.
	ensureInitialBackoff = 2 * time.Second
	// ensureMaxBackoff caps the pause so a cluster that stays broken for hours
	// still retries often enough to recover promptly once it is fixed, without
	// hammering the API server in the meantime.
	ensureMaxBackoff = 2 * time.Minute
	// ensureBackoffFactor grows the pause between attempts.
	ensureBackoffFactor = 2
	// ensureBackoffJitter spreads retries so several agents restarting together
	// do not synchronise their attempts.
	ensureBackoffJitter = 0.2
	// ensureAttemptTimeout bounds a single attempt, so a hung API call fails and
	// is retried rather than parking the loop forever.
	ensureAttemptTimeout = 30 * time.Second
)

// EnsureRunnable wraps Ensure in a manager.Runnable that keeps retrying until
// it succeeds or the manager shuts down.
//
// The retry is not defensive politeness, it is required. The umbrella chart
// applies the stackdome-errors middleware to every router on both Traefik
// entrypoints, and Traefik fails a router whose middleware cannot be resolved.
// The cluster is therefore fail-closed on this object existing: a single
// transient API error during start-up, or CRDs not yet established, used to
// turn into a permanent cluster-wide ingress outage announced by exactly one
// log line. Running as a Runnable also moves the work off the pre-Start path,
// so a slow API server no longer holds up every controller.
func EnsureRunnable(c client.Client, namespace string, selector map[string]string) manager.Runnable {
	return &ensureRunner{
		client:         c,
		namespace:      namespace,
		selector:       selector,
		initialBackoff: ensureInitialBackoff,
		maxBackoff:     ensureMaxBackoff,
	}
}

type ensureRunner struct {
	client    client.Client
	namespace string
	selector  map[string]string

	initialBackoff time.Duration
	maxBackoff     time.Duration
}

// NeedLeaderElection reports true.
//
// Ensure is idempotent, so running it on every replica would be correct — but
// the objects it creates are cluster-wide singletons, and concurrent
// CreateOrUpdate calls from several replicas produce nothing but resource
// conflicts and wasted retries. Gating on the leader lease removes those races
// at the cost of the few seconds the lease takes to acquire, which is well
// inside the window in which Traefik is still starting anyway. This is the
// opposite choice to the error-page HTTP server, which must run on every
// replica because the Service selects them all.
func (*ensureRunner) NeedLeaderElection() bool { return true }

// Start makes an immediate first attempt and then retries with jittered
// exponential backoff. It never returns an error: failing here would take the
// whole manager down, and the agent's other controllers are still worth
// running even when the Traefik objects cannot be created.
func (e *ensureRunner) Start(ctx context.Context) error {
	log := logf.FromContext(ctx).WithName("errorpages")

	delay := e.initialBackoff
	for attempt := 1; ; attempt++ {
		// Bound each attempt: an API call that hangs would otherwise stall the
		// retry loop indefinitely, which is the failure this Runnable exists to
		// rule out.
		attemptCtx, cancel := context.WithTimeout(ctx, ensureAttemptTimeout)
		err := Ensure(attemptCtx, e.client, e.namespace, e.selector)
		cancel()
		if err == nil {
			log.Info("error page objects ensured", "namespace", e.namespace, "attempts", attempt)
			return nil
		}

		pause := wait.Jitter(delay, ensureBackoffJitter)
		log.Error(err, "unable to set up error pages; every Traefik router references the middleware, so retrying until it lands",
			"namespace", e.namespace, "attempt", attempt, "retryIn", pause)

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(pause):
		}

		if delay = delay * ensureBackoffFactor; delay > e.maxBackoff {
			delay = e.maxBackoff
		}
	}
}
