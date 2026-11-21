// Copyright (c) 2026, WSO2 LLC. (https://www.wso2.com).
//
// WSO2 LLC. licenses this file to you under the Apache License,
// Version 2.0 (the "License"); you may not use this file except
// in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied. See the License for the
// specific language governing permissions and limitations
// under the License.

package handler

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/wso2/fhir-server/internal/store"
)

func (h *fhirHandler) startReindex(w http.ResponseWriter, r *http.Request) {
	if !requireFHIRContent(w, r) {
		return
	}
	body, err := h.readFHIRBody(r)
	if err != nil && !errors.Is(err, io.EOF) {
		writeBodyError(w, "invalid reindex body: ", err)
		return
	}
	options, err := h.reindexOptions(body, errors.Is(err, io.EOF), isStructuredContentType(r))
	if err != nil {
		operationOutcome(w, http.StatusBadRequest, "error", "invalid", err.Error())
		return
	}
	job, err := h.store.StartReindex(r.Context(), options)
	if err != nil {
		var conflict store.ConflictError
		if errors.As(err, &conflict) {
			operationOutcome(w, http.StatusConflict, "error", "conflict", err.Error())
			return
		}
		handleError(w, err)
		return
	}
	location := h.tenantBaseURL(r.Context()) + "/_operations/reindex/" + job.ID
	w.Header().Set("Location", location)
	w.Header().Set("Content-Location", location)
	writeFHIR(w, r, http.StatusAccepted, reindexParameters(job))
}

func (h *fhirHandler) reindexOptions(body map[string]any, empty, structured bool) (store.ReindexOptions, error) {
	options := store.ReindexOptions{}
	if empty {
		return options, nil
	}
	if body["resourceType"] != "Parameters" {
		return options, fmt.Errorf("reindex body must be a Parameters resource")
	}
	raw, exists := body["parameter"]
	if !exists {
		return options, nil
	}
	parameters, ok := raw.([]any)
	if !ok {
		return options, fmt.Errorf("parameter must be an array")
	}
	seen := make(map[string]bool)
	for _, raw := range parameters {
		p, ok := raw.(map[string]any)
		if !ok {
			return options, fmt.Errorf("each parameter must be an object")
		}
		name, ok := p["name"].(string)
		if !ok || seen[name] {
			return options, fmt.Errorf("parameters must have unique names")
		}
		seen[name] = true
		switch name {
		case "resourceType":
			resourceType, ok := p["valueCode"].(string)
			if !ok || resourceType == "" || len(p) != 2 {
				return options, fmt.Errorf("resourceType requires a valueCode")
			}
			_, known := r4SummaryElements[resourceType]
			if !known && h.registry != nil {
				known = len(h.registry.ForResource(resourceType)) > 0
			}
			if !known || resourceType == "Resource" || resourceType == "DomainResource" {
				return options, fmt.Errorf("unknown resourceType %q", resourceType)
			}
			options.ResourceType = resourceType
		case "batchSize":
			batchSize, ok := p["valueInteger"].(float64)
			if value, isString := p["valueInteger"].(string); structured && isString {
				parsed, err := strconv.Atoi(value)
				batchSize, ok = float64(parsed), err == nil
			}
			if !ok || batchSize < 1 || batchSize > store.MaxReindexBatchSize || batchSize != float64(int(batchSize)) || len(p) != 2 {
				return options, fmt.Errorf("batchSize requires a valueInteger between 1 and %d", store.MaxReindexBatchSize)
			}
			options.BatchSize = int(batchSize)
		default:
			return options, fmt.Errorf("unknown reindex parameter %q", name)
		}
	}
	return options, nil
}

func (h *fhirHandler) reindexStatus(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "jobId")
	jobID, err := uuid.Parse(id)
	if err != nil {
		operationOutcome(w, http.StatusNotFound, "error", "not-found", "reindex job not found")
		return
	}
	job, err := h.store.GetReindex(r.Context(), jobID.String())
	if err != nil {
		handleError(w, err)
		return
	}
	writeFHIR(w, r, http.StatusOK, reindexParameters(job))
}

func reindexParameters(job store.ReindexJob) map[string]any {
	parameters := []any{
		map[string]any{"name": "id", "valueString": job.ID},
		map[string]any{"name": "status", "valueString": job.Status},
		map[string]any{"name": "batchSize", "valueInteger": job.BatchSize},
		map[string]any{"name": "resourcesReindexed", "valueInteger": job.ResourcesReindexed},
		map[string]any{"name": "queuedTime", "valueInstant": job.QueuedTime.Format(time.RFC3339Nano)},
		map[string]any{"name": "lastModified", "valueInstant": job.LastModified.Format(time.RFC3339Nano)},
	}
	if job.ResourceType != "" {
		parameters = append(parameters, map[string]any{"name": "resourceType", "valueCode": job.ResourceType})
	}
	if job.StartTime != nil {
		parameters = append(parameters, map[string]any{"name": "startTime", "valueInstant": job.StartTime.Format(time.RFC3339Nano)})
	}
	if job.EndTime != nil {
		parameters = append(parameters, map[string]any{"name": "endTime", "valueInstant": job.EndTime.Format(time.RFC3339Nano)})
	}
	if job.Error != "" {
		parameters = append(parameters, map[string]any{"name": "error", "valueString": job.Error})
	}
	return map[string]any{"resourceType": "Parameters", "parameter": parameters}
}
