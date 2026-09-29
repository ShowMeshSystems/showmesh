package enrollment

import (
	"bytes"
	"fmt"

	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// reservedBrokerUsernames are the fixed broker roles no node may use, the
// list deploy/mosquitto/generate-credentials.sh's is_fixed_role holds.
var reservedBrokerUsernames = map[string]bool{
	"coordinator": true, "fpp": true, "healthcheck": true, "observer": true,
}

// IsReservedNodeID reports whether nodeID names a fixed broker role.
func IsReservedNodeID(nodeID string) bool { return reservedBrokerUsernames[nodeID] }

// renderACL builds acl.generated.conf from acl.conf and the password file
// by the rules of render_acl in deploy/mosquitto/generate-credentials.sh,
// byte for byte.
func renderACL(base, passwd []byte) ([]byte, error) {
	var out bytes.Buffer
	out.Write(base)
	for _, username := range passwdUsernames(passwd) {
		if username == "" || reservedBrokerUsernames[username] {
			continue
		}
		if mqttproto.ValidateNodeID(username) != nil {
			return nil, unavailable("The broker password file has a login named %q, which is not a valid node ID. Remove or rename that line in the password file, then try again.", username)
		}
		fmt.Fprintf(&out, `
# Provisioned ShowMesh agent: %[1]s
user %[1]s
topic write showmesh/nodes/%[1]s/hello
topic write showmesh/nodes/%[1]s/lwt
topic write showmesh/nodes/%[1]s/observed/#
topic write showmesh/nodes/%[1]s/result/+
topic read  showmesh/nodes/%[1]s/cmd
topic read  showmesh/events/show_mode
topic read  showmesh/events/weather_delay
`, username)
	}
	return out.Bytes(), nil
}
