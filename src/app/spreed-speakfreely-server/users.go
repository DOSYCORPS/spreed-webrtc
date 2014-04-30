/*
 * Spreed Speak Freely.
 * Copyright (C) 2013-2014 struktur AG
 *
 * This file is part of Spreed Speak Freely.
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 *
 */

package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/satori/go.uuid"
	"github.com/strukturag/phoenix"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type UsersHandler interface {
	Validate(snr *SessionNonceRequest, request *http.Request) (string, error)
	Create(snr *UserNonce, request *http.Request) (*UserNonce, error)
}

type UsersSharedsecretHandler struct {
	secret []byte
}

func (uh *UsersSharedsecretHandler) createHMAC(useridCombo string) string {

	m := hmac.New(sha256.New, uh.secret)
	m.Write([]byte(useridCombo))
	return base64.StdEncoding.EncodeToString(m.Sum(nil))

}

func (uh *UsersSharedsecretHandler) Validate(snr *SessionNonceRequest, request *http.Request) (string, error) {

	// Parse UseridCombo.
	useridCombo := strings.SplitN(snr.UseridCombo, ":", 2)
	expirationString, userid := useridCombo[0], useridCombo[1]

	expiration, err := strconv.ParseInt(expirationString, 10, 64)
	if err != nil {
		return "", err
	}

	// Check expiration.
	if time.Unix(expiration, 0).Before(time.Now()) {
		return "", errors.New("expired secret")
	}

	secret := uh.createHMAC(snr.UseridCombo)
	if snr.Secret != secret {
		return "", errors.New("invalid secret")
	}

	return userid, nil

}

func (uh *UsersSharedsecretHandler) Create(un *UserNonce, request *http.Request) (*UserNonce, error) {

	// TODO(longsleep): Make this configureable - One year for now ...
	expiration := time.Now().Add(time.Duration(1) * time.Hour * 24 * 31 * 12)
	un.Timestamp = expiration.Unix()
	un.UseridCombo = fmt.Sprintf("%d:%s", un.Timestamp, un.Userid)
	un.Secret = uh.createHMAC(un.UseridCombo)

	return un, nil

}

type UsersHTTPHeaderHandler struct {
	headerName string
}

func (uh *UsersHTTPHeaderHandler) Validate(snr *SessionNonceRequest, request *http.Request) (string, error) {

	userid := request.Header.Get(uh.headerName)
	if userid == "" {
		return "", errors.New("no userid provided")
	}
	return userid, nil

}

func (uh *UsersHTTPHeaderHandler) Create(un *UserNonce, request *http.Request) (*UserNonce, error) {

	return nil, errors.New("create is not possible in httpheader mode")

}

type UserNonce struct {
	Nonce       string `json:"nonce"`
	Userid      string `json:"userid"`
	UseridCombo string `json:"useridcombo"`
	Secret      string `json:"secret"`
	Timestamp   int64  `json:"timestamp"`
	Success     bool   `json:"success"`
}

type Users struct {
	hub     *Hub
	realm   string
	handler UsersHandler
}

func NewUsers(hub *Hub, realm string, runtime phoenix.Runtime) *Users {

	var handler UsersHandler

	mode, _ := runtime.GetString("users", "mode")
	switch mode {
	case "sharedsecret":
		secret, _ := runtime.GetString("users", "sharedsecret_secret")
		if secret != "" {
			handler = &UsersSharedsecretHandler{secret: []byte(secret)}
		}
	case "httpheader":
		headerName, _ := runtime.GetString("users", "httpheader_header")
		if headerName == "" {
			headerName = "x-users"
		}
		handler = &UsersHTTPHeaderHandler{headerName: headerName}
	default:
		mode = ""
	}

	if handler == nil {
		handler = &UsersSharedsecretHandler{secret: []byte("")}
	}

	log.Printf("Enabled users handler '%s'\n", mode)

	return &Users{
		hub:     hub,
		realm:   realm,
		handler: handler,
	}

}

// Post is used to create new userids for this server.
func (users *Users) Post(request *http.Request) (int, interface{}, http.Header) {

	decoder := json.NewDecoder(request.Body)
	var snr SessionNonceRequest
	err := decoder.Decode(&snr)
	if err != nil {
		return 400, NewApiError("users_bad_request", "Failed to parse request"), http.Header{"Content-Type": {"application/json"}}
	}

	// Make sure that we have a Sid.
	if snr.Sid == "" || snr.Id == "" {
		return 400, NewApiError("users_bad_request", "Incomplete request"), http.Header{"Content-Type": {"application/json"}}
	}

	// Do this before session validation to avoid timing information.
	userid := fmt.Sprintf("%s@%s", uuid.NewV4().String(), users.realm)

	// Make sure Sid matches session and is valid.
	if !users.hub.ValidateSession(snr.Id, snr.Sid) {
		return 403, NewApiError("users_invalid_session", "Invalid session"), http.Header{"Content-Type": {"application/json"}}
	}

	nonce, err := users.hub.sessiontokenHandler(&SessionToken{Id: snr.Id, Sid: snr.Sid, Userid: userid})
	if err != nil {
		return 400, NewApiError("users_request_failed", fmt.Sprintf("Error: %q", err)), http.Header{"Content-Type": {"application/json"}}
	}

	un, err := users.handler.Create(&UserNonce{Nonce: nonce, Userid: userid, Success: true}, request)
	if err != nil {
		return 400, NewApiError("users_create_failed", fmt.Sprintf("Error: %q", err)), http.Header{"Content-Type": {"application/json"}}
	}

	log.Printf("Users create successfull %s -> %s\n", snr.Id, un.Userid)
	return 200, un, http.Header{"Content-Type": {"application/json"}}

}