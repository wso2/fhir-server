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
	"context"

	"github.com/wso2/fhir-server/internal/store"
)

// StoreAPI is satisfied by *store.Store; extracted here so handlers can be
// tested without a real database.
type StoreAPI interface {
	Read(ctx context.Context, resourceType, resourceID string) (map[string]any, error)
	GetVersion(ctx context.Context, resourceType, resourceID string, versionID int) (map[string]any, error)
	Create(ctx context.Context, resourceType string, body map[string]any) (map[string]any, error)
	Update(ctx context.Context, resourceType, resourceID string, body map[string]any, ifMatchVersion int) (map[string]any, error)
	Patch(ctx context.Context, resourceType, resourceID string, patch map[string]any) (map[string]any, error)
	Delete(ctx context.Context, resourceType, resourceID string) error
	GetHistory(ctx context.Context, resourceType, resourceID string) ([]store.HistoryEntry, error)
	GetTypeHistory(ctx context.Context, p store.HistoryParams) (store.HistoryResult, error)
	Search(ctx context.Context, sp store.SearchParams) (store.SearchResult, error)
	LastN(ctx context.Context, params map[string][]string, maxN int) (store.SearchResult, error)
	ConditionalMatch(ctx context.Context, resourceType, rawQuery string) (string, int, error)
	FetchReferences(ctx context.Context, resourceType, resourceID string, reverse bool) ([]map[string]any, error)
	SyncSearchParameter(ctx context.Context, body map[string]any) error
	DeleteSearchParameter(ctx context.Context, resourceID string) error
	ExecuteBundle(ctx context.Context, bundleType, baseURL string, entries []store.BundleEntryRequest) ([]store.BundleEntryResult, error)
}
