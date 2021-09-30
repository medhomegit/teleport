/*
Copyright 2021 Gravitational, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package db

import (
	"context"

	"github.com/gravitational/teleport"
	"github.com/gravitational/teleport/api/types"
	"github.com/gravitational/teleport/lib/services"
	"github.com/gravitational/teleport/lib/srv/db/cloud/watchers"

	"github.com/gravitational/trace"
)

// startReconciler starts reconciler that registers/unregisters proxied
// databases according to the up-to-date list of database resources and
// databases imported from the cloud.
func (s *Server) startReconciler(ctx context.Context) error {
	reconciler, err := services.NewReconciler(services.ReconcilerConfig{
		Matcher:      s.matcher,
		GetResources: s.getResources,
		OnCreate:     s.onCreate,
		OnUpdate:     s.onUpdate,
		OnDelete:     s.onDelete,
		Log:          s.log,
	})
	if err != nil {
		return trace.Wrap(err)
	}
	go func() {
		for {
			select {
			case <-s.reconcileCh:
				if err := reconciler.Reconcile(ctx, s.getReconcileDatabases()); err != nil {
					s.log.WithError(err).Error("Failed to reconcile.")
				} else if s.cfg.OnReconcile != nil {
					s.cfg.OnReconcile(s.getProxiedDatabases())
				}
			case <-ctx.Done():
				s.log.Debug("Reconciler done.")
				return
			}
		}
	}()
	return nil
}

// startDynamicDatabasesWatcher starts watching changes to database resources and
// registers/unregisters the proxied databases accordingly.
func (s *Server) startDynamicDatabasesWatcher(ctx context.Context) (*services.DatabaseWatcher, error) {
	if len(s.cfg.Selectors) == 0 {
		s.log.Debug("Not starting database resource watcher.")
		return nil, nil
	}
	s.log.Debug("Starting database resource watcher.")
	watcher, err := services.NewDatabaseWatcher(ctx, services.DatabaseWatcherConfig{
		ResourceWatcherConfig: services.ResourceWatcherConfig{
			Component: teleport.ComponentDatabase,
			Log:       s.log,
			Client:    s.cfg.AccessPoint,
		},
	})
	if err != nil {
		return nil, trace.Wrap(err)
	}
	go func() {
		defer s.log.Debug("Database resource watcher done.")
		defer watcher.Close()
		for {
			select {
			case databases := <-watcher.DatabasesC:
				s.setDynamicDatabases(databases)
				select {
				case s.reconcileCh <- struct{}{}:
				case <-ctx.Done():
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	return watcher, nil
}

// startCloudDatabasesWatcher starts fetching cloud databases according to the
// selectors and register/unregister them appropriately.
func (s *Server) startCloudDatabasesWatcher(ctx context.Context) error {
	watcher, err := watchers.NewWatcher(ctx, watchers.WatcherConfig{
		Selectors: s.cfg.Selectors,
		Clients:   s.cfg.CloudClients,
	})
	if err != nil {
		if trace.IsNotFound(err) {
			s.log.Debugf("Not starting cloud database watcher: %v.", err)
			return nil
		}
		return trace.Wrap(err)
	}
	go watcher.Start()
	go func() {
		defer s.log.Debug("Cloud database watcher done.")
		for {
			select {
			case databases := <-watcher.DatabasesC():
				s.setCloudDatabases(databases)
				select {
				case s.reconcileCh <- struct{}{}:
				case <-ctx.Done():
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	return nil
}

func (s *Server) setDynamicDatabases(databases types.Databases) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dynamicDatabases = databases
}

func (s *Server) setCloudDatabases(databases types.Databases) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cloudDatabases = databases
}

// getReconcileDatabases returns a list of databases the reconciler will be
// comparing the currently proxied databases against.
func (s *Server) getReconcileDatabases() types.ResourcesWithLabels {
	s.mu.RLock()
	defer s.mu.RUnlock()
	allDatabases := s.cfg.Databases
	allDatabases = append(allDatabases, s.dynamicDatabases...)
	allDatabases = append(allDatabases, s.cloudDatabases...)
	return allDatabases.AsResources()
}

// getResources returns proxied databases with the as resources.
func (s *Server) getResources() (resources types.ResourcesWithLabels) {
	for _, database := range s.getProxiedDatabases() {
		resources = append(resources, database)
	}
	return resources
}

// onCreate is called by reconciler when a new database is created.
func (s *Server) onCreate(ctx context.Context, resource types.ResourceWithLabels) error {
	database, ok := resource.(types.Database)
	if !ok {
		return trace.BadParameter("expected types.Database, got %T", resource)
	}
	return s.registerDatabase(ctx, database)
}

// onUpdate is called by reconciler when an already proxied database is updated.
func (s *Server) onUpdate(ctx context.Context, resource types.ResourceWithLabels) error {
	database, ok := resource.(types.Database)
	if !ok {
		return trace.BadParameter("expected types.Database, got %T", resource)
	}
	return s.updateDatabase(ctx, database)
}

// onDelete is called by reconciler when a proxied database is deleted.
func (s *Server) onDelete(ctx context.Context, resource types.ResourceWithLabels) error {
	database, ok := resource.(types.Database)
	if !ok {
		return trace.BadParameter("expected types.Database, got %T", resource)
	}
	return s.unregisterDatabase(ctx, database)
}

// matcher is used by reconciler to check if database matches selectors.
func (s *Server) matcher(resource types.ResourceWithLabels) bool {
	database, ok := resource.(types.Database)
	if !ok {
		return false
	}
	if database.IsRDS() || database.IsRedshift() {
		return true // Cloud fetchers return only matching databases.
	}
	return services.MatchResourceLabels(s.cfg.Selectors, database)
}
