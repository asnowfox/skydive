/*
 * Copyright (C) 2016 Red Hat, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy ofthe License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specificlanguage governing permissions and
 * limitations under the License.
 *
 */

// Package server Skydive API
//
// The Skydive REST API allows to communicate with a Skydive analyzer.
//
//     Schemes: http, https
//     Host: localhost:8082
//     BasePath: /api
//     Version: 0.24.0
//     License: Apache http://opensource.org/licenses/Apache-2.0
//     Contact: Skydive mailing list <skydive-dev@redhat.com>
//
//     Consumes:
//     - application/json
//
//     Produces:
//     - application/json
//     - text/plain
//
// swagger:meta
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"
	"time"

	yaml "gopkg.in/yaml.v2"

	auth "github.com/abbot/go-http-auth"
	etcd "github.com/coreos/etcd/client"

	"github.com/skydive-project/skydive/common"
	shttp "github.com/skydive-project/skydive/http"
	"github.com/skydive-project/skydive/logging"
	"github.com/skydive-project/skydive/rbac"
	"github.com/skydive-project/skydive/validator"
	"github.com/skydive-project/skydive/version"
)

// ErrDuplicatedResource is returned when a resource is duplicated
var ErrDuplicatedResource = errors.New("Duplicated resource")

// Server object are created once for each ServiceType (agent or analyzer)
type Server struct {
	HTTPServer *shttp.Server
	EtcdKeyAPI etcd.KeysAPI
	handlers   map[string]Handler
}

// Info for each host describes his API version and service (agent or analyzer)
// swagger:model
type Info struct {
	// Server host ID
	Host string
	// API version
	Version string
	// Service type
	Service string
}

// HandlerFunc describes an http(s) router handler callback function
type HandlerFunc func(w http.ResponseWriter, r *http.Request)

func writeError(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "text/plain; charset=UTF-8")
	w.WriteHeader(status)
	w.Write([]byte(err.Error()))
}

// RegisterAPIHandler registers a new handler for an API
func (a *Server) RegisterAPIHandler(handler Handler, authBackend shttp.AuthenticationBackend) error {
	name := handler.Name()
	title := strings.Title(name)

	routes := []shttp.Route{
		{
			Name:   title + "Index",
			Method: "GET",
			Path:   "/api/" + name,
			HandlerFunc: func(w http.ResponseWriter, r *auth.AuthenticatedRequest) {
				if !rbac.Enforce(r.Username, name, "read") {
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}

				w.Header().Set("Content-Type", "application/json; charset=UTF-8")
				w.WriteHeader(http.StatusOK)

				resources := handler.Index()
				for _, resource := range resources {
					handler.Decorate(resource)
				}

				if err := json.NewEncoder(w).Encode(resources); err != nil {
					logging.GetLogger().Criticalf("Failed to display %s: %s", name, err)
				}
			},
		},
		{
			Name:   title + "Show",
			Method: "GET",
			Path:   shttp.PathPrefix(fmt.Sprintf("/api/%s/", name)),
			HandlerFunc: func(w http.ResponseWriter, r *auth.AuthenticatedRequest) {
				if !rbac.Enforce(r.Username, name, "read") {
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}

				id := r.URL.Path[len(fmt.Sprintf("/api/%s/", name)):]
				if id == "" {
					w.WriteHeader(http.StatusBadRequest)
					return
				}

				resource, ok := handler.Get(id)
				if !ok {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				w.Header().Set("Content-Type", "application/json; charset=UTF-8")
				w.WriteHeader(http.StatusOK)
				handler.Decorate(resource)
				if err := json.NewEncoder(w).Encode(resource); err != nil {
					logging.GetLogger().Criticalf("Failed to display %s: %s", name, err)
				}
			},
		},
		{
			Name:   title + "Insert",
			Method: "POST",
			Path:   "/api/" + name,
			HandlerFunc: func(w http.ResponseWriter, r *auth.AuthenticatedRequest) {
				if !rbac.Enforce(r.Username, name, "write") {
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}

				resource := handler.New()

				var err error
				if contentType := r.Header.Get("Content-Type"); contentType == "application/yaml" {
					if content, e := ioutil.ReadAll(r.Body); e == nil {
						err = yaml.Unmarshal(content, resource)
					} else {
						writeError(w, http.StatusBadRequest, err)
						return
					}
				} else {
					err = common.JSONDecode(r.Body, &resource)
				}
				if err != nil {
					writeError(w, http.StatusBadRequest, err)
					return
				}

				if err := validator.Validate(resource); err != nil {
					writeError(w, http.StatusBadRequest, err)
					return
				}

				var createOpts CreateOptions
				if ttlHeader := r.Header.Get("X-Resource-TTL"); ttlHeader != "" {
					if createOpts.TTL, err = time.ParseDuration(ttlHeader); err != nil {
						writeError(w, http.StatusBadRequest, fmt.Errorf("invalid ttl: %s", err))
						return
					}
				}

				if err := handler.Create(resource, &createOpts); err == ErrDuplicatedResource {
					writeError(w, http.StatusConflict, err)
					return
				} else if err != nil {
					writeError(w, http.StatusBadRequest, err)
					return
				}

				data, err := json.Marshal(&resource)
				if err != nil {
					writeError(w, http.StatusBadRequest, err)
					return
				}

				w.Header().Set("Content-Type", "application/json; charset=UTF-8")
				w.WriteHeader(http.StatusCreated)
				if _, err := w.Write(data); err != nil {
					logging.GetLogger().Criticalf("Failed to create %s: %s", name, err)
				}
			},
		},
		{
			Name:   title + "Delete",
			Method: "DELETE",
			Path:   shttp.PathPrefix(fmt.Sprintf("/api/%s/", name)),
			HandlerFunc: func(w http.ResponseWriter, r *auth.AuthenticatedRequest) {
				if !rbac.Enforce(r.Username, name, "write") {
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}

				id := r.URL.Path[len(fmt.Sprintf("/api/%s/", name)):]
				if id == "" {
					w.WriteHeader(http.StatusBadRequest)
					return
				}

				if err := handler.Delete(id); err != nil {
					if err, ok := err.(etcd.Error); ok && err.Code == etcd.ErrorCodeKeyNotFound {
						writeError(w, http.StatusNotFound, err)
					} else {
						writeError(w, http.StatusBadRequest, err)
					}
					return
				}

				w.WriteHeader(http.StatusOK)
			},
		},
	}

	a.HTTPServer.RegisterRoutes(routes, authBackend)

	if _, err := a.EtcdKeyAPI.Set(context.Background(), "/"+name, "", &etcd.SetOptions{Dir: true}); err != nil {
		if _, err = a.EtcdKeyAPI.Get(context.Background(), "/"+name, nil); err != nil {
			return err
		}
	}

	a.handlers[handler.Name()] = handler

	return nil
}

func (a *Server) addAPIRootRoute(service common.Service, authBackend shttp.AuthenticationBackend) {
	// swagger:operation GET / getApi
	//
	// Get API version
	//
	// ---
	// summary: Get API info
	//
	// tags:
	// - API Info
	//
	// consumes:
	// - application/json
	//
	// produces:
	// - application/json
	//
	// schemes:
	// - http
	// - https
	//
	// responses:
	//   200:
	//     description: API info
	//     schema:
	//       $ref: '#/definitions/Info'

	info := Info{
		Version: version.Version,
		Service: string(service.Type),
		Host:    service.ID,
	}

	routes := []shttp.Route{
		{
			Name:   "Skydive API",
			Method: "GET",
			Path:   "/api",
			HandlerFunc: func(w http.ResponseWriter, r *auth.AuthenticatedRequest) {
				w.Header().Set("Content-Type", "application/json; charset=UTF-8")
				w.WriteHeader(http.StatusOK)

				if err := json.NewEncoder(w).Encode(&info); err != nil {
					logging.GetLogger().Criticalf("Failed to display /api: %s", err)
				}
			},
		}}

	a.HTTPServer.RegisterRoutes(routes, authBackend)
}

// GetHandler returns the hander named hname
func (a *Server) GetHandler(hname string) Handler {
	return a.handlers[hname]
}

// NewAPI creates a new API server based on http
func NewAPI(server *shttp.Server, kapi etcd.KeysAPI, service common.Service, authBackend shttp.AuthenticationBackend) (*Server, error) {
	apiServer := &Server{
		HTTPServer: server,
		EtcdKeyAPI: kapi,
		handlers:   make(map[string]Handler),
	}

	apiServer.addAPIRootRoute(service, authBackend)

	return apiServer, nil
}
