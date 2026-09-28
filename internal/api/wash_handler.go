package api

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/wyw14/solo-038/internal/service"
	"github.com/wyw14/solo-038/internal/wash"
)

// washLedger returns every color change with its fluid mass balance, aggregate
// totals and the changeovers carrying an unexplained loss.
func (handler *Handler) washLedger(writer http.ResponseWriter, request *http.Request) {
	writeJSON(writer, http.StatusOK, handler.runtime.WashLedger())
}

// washSwitch returns one changeover: balance, raw records and correction
// chains so a corrected reading is auditable back to its original and reason.
func (handler *Handler) washSwitch(writer http.ResponseWriter, request *http.Request) {
	detail, err := handler.runtime.WashSwitchDetail(chi.URLParam(request, "switchID"))
	if err != nil {
		writeWashError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, detail)
}

// recordWeighing books a replenish/recovered/waste measurement.
func (handler *Handler) recordWeighing(writer http.ResponseWriter, request *http.Request) {
	var input service.WeighRequest
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	entry, err := handler.runtime.RecordWeighing(input)
	if err != nil {
		writeWashError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, entry)
}

// correctWeighing appends a correction; the original record and the reason are
// both retained.
func (handler *Handler) correctWeighing(writer http.ResponseWriter, request *http.Request) {
	var input service.CorrectionRequest
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	entry, err := handler.runtime.CorrectWeighing(input)
	if err != nil {
		writeWashError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, entry)
}

// writeWashError maps an unknown switch/entry to 404; every other ledger
// rejection is a validation failure and returns 400.
func writeWashError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, wash.ErrSwitchNotFound), errors.Is(err, wash.ErrEntryNotFound):
		writeError(writer, http.StatusNotFound, err)
	default:
		writeError(writer, http.StatusBadRequest, err)
	}
}
