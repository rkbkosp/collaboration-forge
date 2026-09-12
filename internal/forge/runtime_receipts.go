package forge

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
)

// Responses are ephemeral and never restore execution. Tombstones fence old
// request IDs even when large response bodies have been evicted.
type supervisedReceipt struct {
	fingerprint [32]byte
	status      int
	body        []byte
}

func (m *supervisedRuntime) command(h harnessProfile, w http.ResponseWriter, t *supervisedActor, in supervisedCommand) {
	if !attributionID(in.RequestID) {
		h.fail(w, 400, "request_id_required")
		return
	}
	mutation := in.Operation == "checkout" || in.Operation == "issue_claim" || in.Operation == "issue_close" || in.Operation == "issue_release" || in.Operation == "issue_renew" || in.Operation == "issue_create" || in.Operation == "issue_comment" || in.Operation == "issue_link" || in.Operation == "retry"
	if !mutation {
		if in.Operation == "status" {
			m.refreshActor(t)
		}
		m.execute(w, t, in.Operation, in.Params)
		return
	}
	var params any
	raw := in.Params
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if json.Unmarshal(raw, &params) != nil {
		h.fail(w, 400, "validation")
		return
	}
	fingerprint := sha256.Sum256(runtimeJSON([]any{in.Operation, params}))
	if t.receipts == nil {
		t.receipts = map[string]*supervisedReceipt{}
	}
	receipt := t.receipts[in.RequestID]
	if receipt != nil {
		if receipt.fingerprint != fingerprint {
			h.fail(w, 409, "request_id_conflict")
			return
		}
		if receipt.status != 0 {
			if receipt.body == nil {
				h.fail(w, 410, "result_evicted")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(receipt.status)
			if receipt.status == 200 {
				var result map[string]any
				if json.Unmarshal(receipt.body, &result) == nil {
					result["client_replayed"] = true
					result["current_state_not_refreshed"] = true
					result["client_operation"] = in.Operation
					json.NewEncoder(w).Encode(result)
					return
				}
			}
			w.Write(receipt.body)
			return
		}
	} else {
		if len(t.receipts) >= 4096 {
			h.fail(w, 429, "request_limit")
			return
		}
		receipt = &supervisedReceipt{fingerprint: fingerprint}
		t.receipts[in.RequestID] = receipt
	}
	resolvedOperation := in.Operation
	if in.Operation == "retry" {
		resolvedOperation = t.lastOperation
		if t.pending != nil {
			resolvedOperation = t.pending.op
		}
	}
	out := httptest.NewRecorder()
	if in.Operation == "retry" && t.pending == nil && t.lastExecution != nil {
		var result map[string]any
		json.Unmarshal(t.lastExecution, &result)
		result["client_operation"] = t.lastOperation
		result["client_replayed"] = true
		result["current_state_not_refreshed"] = true
		runtimeReply(out, result)
	} else {
		m.execute(out, t, in.Operation, raw)
	}
	retryable := in.Operation == "issue_claim" || in.Operation == "issue_close" || in.Operation == "issue_release" || in.Operation == "issue_renew" || in.Operation == "retry"
	if out.Code < 500 || !retryable {
		receipt.status = out.Code
		receipt.body = bytesCopy(out.Body.Bytes())
		t.receiptOrder = append(t.receiptOrder, in.RequestID)
		t.receiptBytes += len(receipt.body)
		for len(t.receiptOrder) > 32 || t.receiptBytes > 16<<20 {
			old := t.receipts[t.receiptOrder[0]]
			t.receiptOrder = t.receiptOrder[1:]
			t.receiptBytes -= len(old.body)
			old.body = nil
		}
		if out.Code == 200 && (in.Operation == "issue_claim" || in.Operation == "issue_close" || in.Operation == "issue_release" || in.Operation == "retry") {
			t.lastExecution = bytesCopy(out.Body.Bytes())
			t.lastOperation = resolvedOperation
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(out.Code)
	w.Write(out.Body.Bytes())
}
func bytesCopy(b []byte) []byte { return append([]byte(nil), b...) }
