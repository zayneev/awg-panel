package routing

import "regexp"

var (
	ruleIDPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
	geositePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,127}$`)
	regexpNFTName  = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]{0,31}$`)
)
