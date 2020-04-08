//
// Copyright (c) 2012-2019 Red Hat, Inc.
// This program and the accompanying materials are made
// available under the terms of the Eclipse Public License 2.0
// which is available at https://www.eclipse.org/legal/epl-2.0/
//
// SPDX-License-Identifier: EPL-2.0
//
// Contributors:
//   Red Hat, Inc. - initial API and implementation
//

package main

import (
	"errors"
	"fmt"
	"io/ioutil"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"net/http"
	"os"

	jsonrpc "github.com/eclipse/che-go-jsonrpc"
	"github.com/eclipse/che-go-jsonrpc/jsonrpcws"
	"github.com/eclipse/che-machine-exec/api/events"
	execRpc "github.com/eclipse/che-machine-exec/api/jsonrpc"
	jsonRpcApi "github.com/eclipse/che-machine-exec/api/jsonrpc"
	"github.com/eclipse/che-machine-exec/api/model"
	"github.com/eclipse/che-machine-exec/api/websocket"
	"github.com/eclipse/che-machine-exec/cfg"
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

const (
	namespaceFile = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"
	WsIdLabel     = "che.workspace_id"
)

func main() {
	cfg.Parse()
	cfg.Print()

	r := gin.Default()

	if cfg.StaticPath != "" {
		r.StaticFS("/static", http.Dir(cfg.StaticPath))
		r.GET("/", func(c *gin.Context) {
			c.Redirect(http.StatusMovedPermanently, "/static")
		})
	}

	// connect to exec api end point(websocket with json-rpc)
	r.GET("/connect", func(c *gin.Context) {
		var token string
		if cfg.UseBearerToken {
			token = c.Request.Header.Get("X-Forwarded-Access-Token")
			if len(token) == 0 {
				err := errors.New("unable to find user token header")
				logrus.Debug(err)
				c.JSON(c.Writer.Status(), err.Error())
				return
			}
			username := c.Request.Header.Get("X-Forwarded-User")
			logrus.Info(fmt.Sprintf("User connecting: %s", username))
			if len(username) == 0 {
				logrus.Warn("Could not get username on request")
			} else {
				if !checkUsername(username) {
					err := errors.New("username in request does not match owner")
					logrus.Debug(err)
					c.JSON(c.Writer.Status(), err.Error())
					return
				}
			}
		}

		conn, err := jsonrpcws.Upgrade(c.Writer, c.Request)
		if err != nil {
			c.JSON(c.Writer.Status(), err.Error())
			return
		}

		logrus.Debug("Create json-rpc channel for new websocket connnection")
		tunnel := jsonrpc.NewManagedTunnel(conn)
		if len(token) > 0 {
			tunnel.Attributes[execRpc.BearerTokenAttr] = token
		}

		execConsumer := &events.ExecEventConsumer{Tunnel: tunnel}
		events.EventBus.SubAny(execConsumer, model.OnExecError, model.OnExecExit)

		tunnel.SayHello()
	})

	// attach to get exec output and sent user input(by simple websocket)
	// Todo: rework to use only one websocket connection https://github.com/eclipse/che-machine-exec/issues/4
	r.GET("/attach/:id", func(c *gin.Context) {
		if err := websocket.Attach(c.Writer, c.Request, c.Param("id")); err != nil {
			c.JSON(c.Writer.Status(), err.Error())
		}
	})

	// create json-rpc routs group
	appOpRoutes := []jsonrpc.RoutesGroup{
		jsonRpcApi.RPCRoutes,
	}
	// register routes
	jsonrpc.RegRoutesGroups(appOpRoutes)
	jsonrpc.PrintRoutes(appOpRoutes)

	if err := r.Run(cfg.URL); err != nil {
		logrus.Fatal("Unable to start server. Cause: ", err.Error())
	}
}

func checkUsername(username string) bool {
	if len(username) == 0 {
		logrus.Debug("Failed to create k8sAPI. Token must not be empty")
		return false
	}

	config, err := rest.InClusterConfig()
	if err != nil {
		logrus.Debug(err)
		return false
	}

	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		logrus.Debug(err)
		return false
	}

	workspaceID := os.Getenv("CHE_WORKSPACE_ID")
	if workspaceID == "" {
		err := errors.New("unable to get current workspace id")
		logrus.Debug(err)
		return false
	}

	filterOptions := metav1.ListOptions{LabelSelector: WsIdLabel + "=" + workspaceID, FieldSelector: "status.phase=Running"}
	wsPods, err := client.CoreV1().Pods(readNamespace()).List(filterOptions)
	if err != nil {
		logrus.Debug(err)
		return false
	}
	for _, pod := range wsPods.Items {
		if pod.Annotations["org.eclipse.che.workspace/user"] == username {
			logrus.Info("User matches annotation, continuing")
			return true
		}
	}
	return false
}

func readNamespace() string {
	nsBytes, err := ioutil.ReadFile(namespaceFile)
	if err != nil {
		logrus.Fatal("Failed to get Namespace", err)
	}
	return string(nsBytes)
}
