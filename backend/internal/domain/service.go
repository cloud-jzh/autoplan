// Package domain contains transport- and persistence-independent business types.
// Concrete AutoPlan contracts are introduced only when their compatibility
// schemas are frozen; this package must never import an adapter package.
package domain

// Service identifies an application capability without coupling it to REST,
// SSE, WebSocket, MCP, Electron, or a repository implementation.
type Service string

const (
	ServiceProjects   Service = "projects"
	ServiceSnapshots  Service = "snapshots"
	ServiceOperations Service = "operations"
	ServiceEvents     Service = "events"
)
