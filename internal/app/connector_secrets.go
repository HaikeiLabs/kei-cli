package app

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/HaikeiLabs/kei-connector-contracts/contract"
	"github.com/HaikeiLabs/kei-connector-contracts/setup"
)

// credentialRecipient is a runtime credential-sync key a connector secret is
// sealed to, as listed by GET /api/cli/credential-store/recipients.
type credentialRecipient struct {
	RuntimeInstallationID string `json:"runtime_installation_id"`
	KeyID                 string `json:"key_id"`
	PublicKey             string `json:"public_key"`
}

type credentialRecipients struct {
	CredentialStoreInstallationID string                `json:"credential_store_installation_id"`
	Recipients                    []credentialRecipient `json:"recipients"`
}

type sealedRecipient struct {
	RuntimeInstallationID string `json:"runtime_installation_id"`
	KeyID                 string `json:"key_id"`
	SealedPayload         string `json:"sealed_payload"`
}

// setSecretRequest is the body of POST /api/cli/connectors/{id}:setSecret
// (HAI-262). It carries only ciphertext.
type setSecretRequest struct {
	Field                         string            `json:"field"`
	CredentialStoreInstallationID string            `json:"credential_store_installation_id"`
	Recipients                    []sealedRecipient `json:"recipients"`
}

type secretStatus struct {
	Field       string  `json:"field"`
	Set         bool    `json:"set"`
	Generation  int     `json:"generation"`
	DeliveredAt *string `json:"delivered_at"`
}

// sealConnectorSecret seals a secret to one runtime credential-sync key with
// the KMP1 envelope that kei-console uses for model-profile API keys
// (model_profile_envelope.go) and that kei-proxy credential sync opens: an
// ephemeral X25519 key, AES-256-GCM under SHA-256 of the envelope label and
// the shared secret, and the recipient's runtime installation and key id as
// associated data, so a payload opens only for the key it was sealed to.
func sealConnectorSecret(secret string, recipient credentialRecipient) (string, error) {
	publicBytes, err := base64.RawStdEncoding.DecodeString(recipient.PublicKey)
	if err != nil {
		publicBytes, err = base64.StdEncoding.DecodeString(recipient.PublicKey)
	}
	if err != nil {
		return "", errors.New("decode recipient public key")
	}
	curve := ecdh.X25519()
	publicKey, err := curve.NewPublicKey(publicBytes)
	if err != nil {
		return "", errors.New("invalid recipient public key")
	}
	privateKey, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return "", fmt.Errorf("generate ephemeral key: %w", err)
	}
	shared, err := privateKey.ECDH(publicKey)
	if err != nil {
		return "", errors.New("derive envelope key")
	}
	key := sha256.Sum256(append([]byte("kei-model-profile-envelope-v1\x00"), shared...))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	aad := []byte(recipient.RuntimeInstallationID + "\x00" + recipient.KeyID)
	ciphertext := gcm.Seal(nil, nonce, []byte(secret), aad)
	payload := append([]byte("KMP1"), privateKey.PublicKey().Bytes()...)
	payload = append(payload, nonce...)
	payload = append(payload, ciphertext...)
	return base64.RawStdEncoding.EncodeToString(payload), nil
}

// connectorSecretField is the provider's secret setup field for an account
// model, if it has one.
func connectorSecretField(provider contract.Provider, model contract.AccountModel) (setup.SetupField, bool) {
	schema, ok := setup.SetupSchemaFor(provider)
	if !ok {
		return setup.SetupField{}, false
	}
	for _, field := range schema.FieldsFor(model) {
		if field.Secret {
			return field, true
		}
	}
	return setup.SetupField{}, false
}

// sealForWorkspace seals a secret to every runtime key that can receive
// secrets for the session's workspace. It fails when there is none, before
// anything is created.
func (s *connectorSession) sealForWorkspace(field, secret string) (setSecretRequest, error) {
	payload, err := s.doPath(http.MethodGet, "/api/cli/credential-store/recipients", nil)
	if err != nil {
		return setSecretRequest{}, fmt.Errorf("list credential delivery recipients: %w", err)
	}
	var store credentialRecipients
	if err := json.Unmarshal(payload, &store); err != nil {
		return setSecretRequest{}, fmt.Errorf("decode credential delivery recipients: %w", err)
	}
	if store.CredentialStoreInstallationID == "" || len(store.Recipients) == 0 {
		return setSecretRequest{}, errors.New("no runtime in this workspace can receive secrets; set up a credential store and a runtime with a credential-sync key first")
	}
	request := setSecretRequest{Field: field, CredentialStoreInstallationID: store.CredentialStoreInstallationID}
	for _, recipient := range store.Recipients {
		sealed, err := sealConnectorSecret(secret, recipient)
		if err != nil {
			return setSecretRequest{}, fmt.Errorf("seal secret for runtime %s: %w", recipient.RuntimeInstallationID, err)
		}
		request.Recipients = append(request.Recipients, sealedRecipient{RuntimeInstallationID: recipient.RuntimeInstallationID, KeyID: recipient.KeyID, SealedPayload: sealed})
	}
	return request, nil
}

// setSecret queues delivery of a sealed secret and returns its new generation.
func (s *connectorSession) setSecret(connectorID string, request setSecretRequest) (secretStatus, error) {
	payload, err := s.do(http.MethodPost, "/"+url.PathEscape(connectorID)+":setSecret", request)
	if err != nil {
		return secretStatus{}, err
	}
	var status secretStatus
	if err := json.Unmarshal(payload, &status); err != nil {
		return secretStatus{}, fmt.Errorf("decode setSecret response: %w", err)
	}
	return status, nil
}
