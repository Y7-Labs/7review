package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Y4NN777/7review/agent/accounting"
	"github.com/Y4NN777/7review/agent/tools"
)

const publicationLease = 30 * time.Second

func (s *Server) maintainPublications(ctx context.Context) error {
	if s == nil || s.accountingRuntime == nil || s.accountingRuntime.Authority == nil {
		return nil
	}
	authority := s.accountingRuntime.Authority
	if _, err := authority.MaterializeAuthorizedPublications(ctx, 8); err != nil {
		return err
	}
	if _, err := authority.RecoverUncertainPublications(ctx, 8); err != nil {
		return err
	}
	if s.publicationPublisher == nil || s.publicationWorkerID == "" {
		return nil
	}
	uncertain, err := authority.ListUncertainPublications(ctx, 8)
	if err != nil {
		return err
	}
	for _, operation := range uncertain {
		request := authorizedPublicationRequest(operation)
		receipt, lookupErr := s.publicationPublisher.LookupAuthorized(ctx, request)
		if lookupErr != nil {
			return fmt.Errorf("publication %s reconciliation lookup: %w", operation.ID, lookupErr)
		}
		if receipt == nil {
			continue
		}
		if _, reconcileErr := authority.ReconcilePublication(ctx, operation.ID, accountingPublicationReceipt(*receipt)); reconcileErr != nil {
			return fmt.Errorf("publication %s reconciliation commit: %w", operation.ID, reconcileErr)
		}
	}
	claim, err := authority.ClaimPublication(ctx, s.publicationWorkerID, publicationLease)
	if err != nil || claim == nil {
		return err
	}
	permit, err := authority.BeginPublication(ctx, *claim, publicationLease)
	if errors.Is(err, accounting.ErrPublicationObsolete) {
		return nil
	}
	if err != nil {
		return err
	}
	receipt, err := s.publicationPublisher.PublishAuthorized(ctx, authorizedPublicationRequest(claim.Operation))
	if err != nil {
		return fmt.Errorf("publication %s provider result uncertain: %w", claim.Operation.ID, err)
	}
	if _, err = authority.CompletePublication(ctx, *permit, accountingPublicationReceipt(receipt)); err != nil {
		return fmt.Errorf("publication %s completion commit uncertain: %w", claim.Operation.ID, err)
	}
	return nil
}

func authorizedPublicationRequest(operation accounting.PublicationOperation) tools.AuthorizedPublicationRequest {
	return tools.AuthorizedPublicationRequest{
		OperationID:   operation.ID,
		PayloadDigest: operation.PayloadDigest,
		Destination:   operation.Destination,
		Payload:       string(operation.Payload),
	}
}

func accountingPublicationReceipt(receipt tools.AuthorizedPublicationReceipt) accounting.PublicationDeliveryReceipt {
	return accounting.PublicationDeliveryReceipt{
		Provider:      receipt.Provider,
		RemoteID:      receipt.RemoteID,
		RemoteURL:     receipt.RemoteURL,
		PayloadDigest: receipt.PayloadDigest,
	}
}
