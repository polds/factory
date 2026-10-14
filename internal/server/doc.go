// Package server wires factoryd: REST, SSE, the worker gateway, the scheduler,
// and run lifecycle over the Postgres store.
//
// Task containers are attached to the host Docker bridge, not the compose
// network, so they cannot use the compose service name. FACTORY_TASK_SERVER_URL
// is the base URL those containers use for event push. It defaults to
// http://172.17.0.1:8080, the Linux bridge gateway, with factoryd's port
// published on the host. Override it where that gateway differs (Docker
// Desktop uses http://host.docker.internal:8080).
package server
