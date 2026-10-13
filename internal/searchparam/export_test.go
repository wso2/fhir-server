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

package searchparam

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SetDebounce overrides the watcher's debounce for tests.
func (w *Watcher) SetDebounce(d time.Duration) { w.debounce = d }

// Listening signals once the connection is established.
func (w *Watcher) Listening() <-chan struct{} { return w.listening }

// ReloadForTest drives one reload, as the watch loop would.
func (w *Watcher) ReloadForTest(ctx context.Context) { w.reload(ctx, "test") }

// SetAfterQuery installs a hook that runs between a reload's query and its swap.
func (r *Registry) SetAfterQuery(f func()) { r.afterQuery = f }

// ReloadForTest runs one full reload and returns its error.
func (r *Registry) ReloadForTest(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := r.load(ctx, pool)
	return err
}
