package controlplane

import (
	"net/http"

	"github.com/owainlewis/factory/internal/protocol"
)

func (a *API) getProductUpgrade(w http.ResponseWriter, r *http.Request) {
	upgrade, err := a.store.ProductUpgrade(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, upgrade)
}

func (a *API) applyProductUpgrade(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input protocol.ApplyProductUpgradeRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	upgrade, err := a.store.ApplyProductUpgrade(r.Context(), input.CancelActive)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, upgrade)
}
