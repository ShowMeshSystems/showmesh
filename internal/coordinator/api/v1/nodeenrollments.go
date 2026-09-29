package v1

// Wire shapes under /api/v1/node-enrollments (ADR-055 decisions 4 and 5).

// CreateNodeEnrollmentRequest is the body of POST /node-enrollments.
type CreateNodeEnrollmentRequest struct {
	NodeID           string `json:"nodeId"`
	Reenroll         bool   `json:"reenroll"`
	ExpiresInSeconds *int   `json:"expiresInSeconds,omitempty"`
}

// CreateNodeEnrollmentResponse is the 201 body of POST /node-enrollments,
// the only response that carries the code.
type CreateNodeEnrollmentResponse struct {
	ServerTime     string `json:"serverTime"`
	ID             string `json:"id"`
	NodeID         string `json:"nodeId"`
	Code           string `json:"code"`
	Reenroll       bool   `json:"reenroll"`
	ExpiresAt      string `json:"expiresAt"`
	CoordinatorURL string `json:"coordinatorUrl"`
}

// NodeEnrollment is one code as listed. It never carries the code.
type NodeEnrollment struct {
	ID         string  `json:"id"`
	NodeID     string  `json:"nodeId"`
	Reenroll   bool    `json:"reenroll"`
	CreatedBy  string  `json:"createdBy"`
	CreatedAt  string  `json:"createdAt"`
	ExpiresAt  string  `json:"expiresAt"`
	State      string  `json:"state"`
	RedeemedAt *string `json:"redeemedAt"`
}

// NodeEnrollmentsResponse is the body of GET /node-enrollments.
type NodeEnrollmentsResponse struct {
	ServerTime  string           `json:"serverTime"`
	Enrollments []NodeEnrollment `json:"enrollments"`
}

// NodeEnrollmentResponse is the body of DELETE /node-enrollments/{id}.
type NodeEnrollmentResponse struct {
	ServerTime string         `json:"serverTime"`
	Enrollment NodeEnrollment `json:"enrollment"`
}

// RedeemNodeEnrollmentRequest is the body of POST /node-enrollments/redeem.
type RedeemNodeEnrollmentRequest struct {
	Code     string `json:"code"`
	Hostname string `json:"hostname,omitempty"`
	Arch     string `json:"arch,omitempty"`
}

// RedeemNodeEnrollmentResponse is the 200 body of POST
// /node-enrollments/redeem: everything the node's agent.env needs, once.
type RedeemNodeEnrollmentResponse struct {
	ServerTime           string `json:"serverTime"`
	NodeID               string `json:"nodeId"`
	BrokerURL            string `json:"brokerUrl"`
	MQTTUsername         string `json:"mqttUsername"`
	MQTTPassword         string `json:"mqttPassword"`
	APIToken             string `json:"apiToken"`
	CoordinatorURL       string `json:"coordinatorUrl"`
	CoordinatorPublicKey string `json:"coordinatorPublicKey"`
}
