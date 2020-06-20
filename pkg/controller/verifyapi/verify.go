// Copyright 2020 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package verifyapi implements the exchange of the verification code
// (short term token) for a long term token that can be used to get a
// verification certification to send to the key server.
//
// This is steps 4/5 as specified here:
// https://developers.google.com/android/exposure-notifications/verification-system#flow-diagram
package verifyapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/exposure-notifications-verification-server/pkg/api"
	"github.com/google/exposure-notifications-verification-server/pkg/config"
	"github.com/google/exposure-notifications-verification-server/pkg/controller"
	"github.com/google/exposure-notifications-verification-server/pkg/database"
	"github.com/google/exposure-notifications-verification-server/pkg/jwthelper"
	"github.com/google/exposure-notifications-verification-server/pkg/logging"
	"github.com/google/exposure-notifications-verification-server/pkg/signer"

	"github.com/dgrijalva/jwt-go"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// VerifyAPI is a controller for the verification code verification API.
type VerifyAPI struct {
	config *config.Config
	db     *database.Database
	logger *zap.SugaredLogger
	signer signer.KeyManager
}

func New(ctx context.Context, config *config.Config, db *database.Database, signer signer.KeyManager) controller.Controller {
	return &VerifyAPI{config, db, logging.FromContext(ctx), signer}
}

func (v *VerifyAPI) Execute(c *gin.Context) {
	// APIKey should be verified by middleware.
	var request api.VerifyCodeRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		v.logger.Errorf("failed to bind request: %v", err)
		c.JSON(http.StatusOK, api.Error("invalid request: %v", err))
		return
	}

	// Get the signer based on Key configuration.
	signer, err := v.signer.NewSigner(c.Request.Context(), v.config.TokenSigningKey)
	if err != nil {
		v.logger.Errorf("unable to get signing key: %v", err)
		c.JSON(http.StatusInternalServerError, api.Error("internal server error - unable to sign tokens"))
		return
	}

	// Exchange the short term verification code for a long term verification token.
	// The token can be used to sign TEKs later.
	verificationToken, err := v.db.VerifyCodeAndIssueToken(request.VerificationCode, v.config.VerificationTokenDuration)
	if err != nil {
		v.logger.Errorf("error issuing verification token: %v", err)
		if errors.Is(err, database.ErrVerificationCodeExpired) || errors.Is(err, database.ErrVerificationCodeUsed) {
			c.JSON(http.StatusBadRequest, api.Error(err.Error()))
			return
		}
		c.JSON(http.StatusInternalServerError, api.Error("internal serer error"))
		return
	}

	subject := verificationToken.TestType + "." + verificationToken.FormatTestDate()
	now := time.Now().UTC()
	claims := &jwt.StandardClaims{
		Audience:  v.config.TokenIssuer,
		ExpiresAt: now.Add(v.config.VerificationTokenDuration).Unix(),
		Id:        verificationToken.TokenID,
		IssuedAt:  now.Unix(),
		Issuer:    v.config.TokenIssuer,
		Subject:   subject,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	signedJWT, err := jwthelper.SignJWT(token, signer)
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.Error("error signing token, must obtain new verification code"))
		return
	}

	c.JSON(http.StatusOK, api.VerifyCodeResponse{
		TestType:          verificationToken.TestType,
		TestDate:          verificationToken.FormatTestDate(),
		VerificationToken: signedJWT,
	})
}