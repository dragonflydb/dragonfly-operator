package resources

import _ "embed"

//go:embed scripts/liveness-check.sh
var defaultLivenessScript string

//go:embed scripts/readiness-check.sh
var defaultReadinessScript string

//go:embed scripts/startup-check.sh
var defaultStartupScript string

//go:embed scripts/prestop-takeover.sh
var defaultPreStopTakeoverScript string
