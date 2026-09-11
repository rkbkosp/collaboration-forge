package forge

import (
	"context"
	"go.kenn.io/kata"
)

// Only a typed in-process dispatch can construct this capability. Supplying a
// principal, role header, or raw Kata request never grants worker authority.
type toolCapabilityKey struct{}
type toolCapability struct {
	project   kata.Project
	operation string
	principal kata.Principal
}

func authorizeTool(ctx context.Context, req kata.AccessRequest) (kata.AccessDecision, error) {
	cap, ok := ctx.Value(toolCapabilityKey{}).(toolCapability)
	if !ok || cap.project.ID <= 0 || cap.project.UID == "" || cap.operation == "" || req.Operation.ID != cap.operation || req.Principal != cap.principal {
		return kata.AccessDecision{}, kata.ErrAccessDenied
	}
	for _, id := range req.Operation.ProjectIDs {
		if id != cap.project.ID {
			return kata.AccessDecision{}, kata.ErrAccessDenied
		}
	}
	for _, uid := range req.Operation.ProjectUIDs {
		if uid != cap.project.UID {
			return kata.AccessDecision{}, kata.ErrAccessDenied
		}
	}
	if err := ctx.Err(); err != nil {
		return kata.AccessDecision{}, err
	}
	// Close/graph initially declare AllProjects for dependencies not yet resolved.
	// Kata repeats authorization cumulatively for each discovered project before
	// exposing or mutating it. The fixed operation may start, not bypass scope.
	return kata.AccessDecision{TransactionFence: func(ctx context.Context, _ kata.Transaction) error { return ctx.Err() }}, nil
}
