package api

import (
	"net/http"

	"github.com/wyw14/solo-038/internal/washup"
)

type washupEntryRequest struct {
	Circuit    string  `json:"circuit"`
	Kind       string  `json:"kind"`
	QuantityKG float64 `json:"quantity_kg"`
	SwitchID   string  `json:"switch_id"`
	Barrel     string  `json:"barrel"`
	Operator   string  `json:"operator"`
	Note       string  `json:"note"`
}

type washupCorrectionRequest struct {
	EntryID    string  `json:"entry_id"`
	QuantityKG float64 `json:"quantity_kg"`
	Reason     string  `json:"reason"`
}

func (handler *Handler) createWashupEntry(writer http.ResponseWriter, request *http.Request) {
	var input washupEntryRequest
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	view, err := handler.runtime.RecordWashupEntry(input.Circuit, washup.EntryKind(input.Kind), input.QuantityKG, input.SwitchID, input.Barrel, input.Operator, input.Note)
	if err != nil {
		writeError(writer, http.StatusConflict, err)
		return
	}
	writeJSON(writer, http.StatusCreated, view)
}

func (handler *Handler) createWashupCorrection(writer http.ResponseWriter, request *http.Request) {
	var input washupCorrectionRequest
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	view, err := handler.runtime.CorrectWashupEntry(input.EntryID, input.QuantityKG, input.Reason)
	if err != nil {
		writeError(writer, http.StatusConflict, err)
		return
	}
	writeJSON(writer, http.StatusCreated, view)
}

func (handler *Handler) washupEntries(writer http.ResponseWriter, request *http.Request) {
	writeJSON(writer, http.StatusOK, handler.runtime.WashupEntries(request.URL.Query().Get("circuit")))
}

func (handler *Handler) washupBalance(writer http.ResponseWriter, request *http.Request) {
	writeJSON(writer, http.StatusOK, handler.runtime.WashupBalance(request.URL.Query().Get("circuit")))
}

func (handler *Handler) washupSwitches(writer http.ResponseWriter, request *http.Request) {
	writeJSON(writer, http.StatusOK, handler.runtime.WashupSwitches())
}
