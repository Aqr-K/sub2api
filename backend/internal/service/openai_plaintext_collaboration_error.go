package service

import "errors"

// OpenAIPlaintextCollaborationError is a compatibility failure, not an account
// outage. It never authorizes replaying a request or relabeling ciphertext.
type OpenAIPlaintextCollaborationError struct{ cause error }

func (e *OpenAIPlaintextCollaborationError) Error() string { return e.cause.Error() }
func (e *OpenAIPlaintextCollaborationError) Unwrap() error { return e.cause }

func plaintextCollaborationError(err error) error {
	if err == nil {
		return nil
	}
	var existing *OpenAIPlaintextCollaborationError
	if errors.As(err, &existing) {
		return err
	}
	return &OpenAIPlaintextCollaborationError{cause: err}
}

// IsOpenAIResponseCompatibilityError includes errors for which observed usage
// must survive forwarding, without treating the request as an account failure.
func IsOpenAIResponseCompatibilityError(err error) bool {
	var plaintextErr *OpenAIPlaintextCollaborationError
	return IsOpenAIEncryptedHistoryError(err) || errors.As(err, &plaintextErr)
}
