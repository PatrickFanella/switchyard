package controlplane

import "net/http"

// ControlAccess tells the dashboard which control-plane role served the
// request so it can present read-only screens before an operator edits
// anything. It is advisory: controlPlaneAuth still rejects every non-read
// request from a read-role caller.
type ControlAccess struct {
	Role     string `json:"role"`
	CanWrite bool   `json:"can_write"`
}

// controlAccessFromRequest reports "admin" or "read" behind controlPlaneAuth.
// A handler served without that middleware (tests, embedded use) has no role
// and no authentication boundary, so it is reported as "unrestricted".
func controlAccessFromRequest(r *http.Request) ControlAccess {
	role, _ := r.Context().Value(controlPlaneRoleContextKey{}).(string)
	switch role {
	case "admin":
		return ControlAccess{Role: "admin", CanWrite: true}
	case "read":
		return ControlAccess{Role: "read", CanWrite: false}
	default:
		return ControlAccess{Role: "unrestricted", CanWrite: true}
	}
}

func (a *API) controlAccess(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, controlAccessFromRequest(r))
}
