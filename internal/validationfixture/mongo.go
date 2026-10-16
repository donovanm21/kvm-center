// Package validationfixture contains guards used only by disposable test fixtures.
package validationfixture

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// MongoURI accepts one unauthenticated loopback endpoint and disables replica
// discovery. Driver URI options are rejected so proxies or discovered hosts
// cannot redirect fixture writes outside that endpoint.
func MongoURI(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "mongodb" || u.User != nil || u.Fragment != "" ||
		(u.Path != "" && u.Path != "/") || strings.Contains(u.Host, ",") ||
		(u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" && u.Hostname() != "::1") ||
		(u.RawQuery != "" && u.RawQuery != "directConnection=true") {
		return "", fmt.Errorf("validation MongoDB requires one unauthenticated loopback endpoint without driver options")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", fmt.Errorf("invalid validation MongoDB port")
		}
	}
	u.RawQuery = "directConnection=true"
	return u.String(), nil
}
