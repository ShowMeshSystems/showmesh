package api

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/pkg/capability"
)

// nodeClockPTPReport is what a node's clock.ptp capability says about its
// network interfaces. reported is false for a node that never sent one.
type nodeClockPTPReport struct {
	reported         bool
	interfaces       []string
	externalProvider bool
}

func readNodeClockPTPReport(caps capability.Set) nodeClockPTPReport {
	c, ok := caps.Lookup("clock.ptp")
	if !ok {
		return nodeClockPTPReport{}
	}
	report := nodeClockPTPReport{reported: true}
	items, _ := c.Attributes["interfaces"].([]any)
	for _, item := range items {
		if m, ok := item.(map[string]any); ok {
			if name, ok := m["name"].(string); ok && name != "" {
				report.interfaces = append(report.interfaces, name)
			}
		}
	}
	report.externalProvider, _ = c.Attributes["externalProvider"].(bool)
	return report
}

// checkNodeClockAgainstNode refuses node.clock settings the node's own
// clock.ptp report shows it cannot use. A node that has not reported
// clock.ptp is not checked.
func (h *handlers) checkNodeClockAgainstNode(ctx context.Context, now time.Time, nodeID string, p config.NodeClockPayload) (*config.ValidationError, error) {
	evidence, err := audioNodeCapabilitySet(ctx, h.deps.Nodes, now, nodeID)
	if err != nil {
		return nil, err
	}
	report := readNodeClockPTPReport(evidence.Capabilities)
	if !report.reported || p.Provider == config.NodeClockProviderFPP {
		return nil, nil
	}
	if !slices.Contains(report.interfaces, p.Interface) {
		return &config.ValidationError{Code: config.ValidationCodeFieldInvalid, Field: "interface", Detail: fmt.Sprintf(
			"%s is not a network interface on node %s. Pick one of %s.", p.Interface, nodeID, strings.Join(report.interfaces, ", "))}, nil
	}
	if p.Provider == config.NodeClockProviderManaged && report.externalProvider {
		return &config.ValidationError{Code: config.ValidationCodeFieldInvalid, Field: "provider", Detail: fmt.Sprintf(
			"Node %s already runs its own PTP service, so ShowMesh cannot start another. Use that service instead.", nodeID)}, nil
	}
	return nil, nil
}
