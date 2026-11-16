package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	CopilotPricingStateKey = "CopilotPricingVerification"
	CopilotPricingMaxAge   = int64(24 * 60 * 60)
	CopilotPricingURL      = "https://docs.github.com/en/copilot/reference/copilot-billing/models-and-pricing"
)

type CopilotPricingReference struct {
	ChannelID int    `json:"channel_id"`
	Model     string `json:"model"`
	Version   string `json:"version"`
}

type CopilotPricingSource struct {
	Version       string            `json:"version"`
	StartedAt     int64             `json:"started_at"`
	LastAttemptAt int64             `json:"last_attempt_at"`
	LastSuccessAt int64             `json:"last_success_at"`
	Error         string            `json:"error"`
	Expressions   map[string]string `json:"expressions"`
}

type CopilotPricingState struct {
	Sources   map[int]CopilotPricingSource       `json:"sources"`
	Approvals map[string]CopilotPricingReference `json:"approvals"`
}

var copilotPricingCache = struct {
	sync.Mutex
	raw   string
	state *CopilotPricingState
	err   error
}{}

func CopilotPricingVersion(model, expression string) string {
	sum := sha256.Sum256([]byte(model + "\n" + expression))
	return hex.EncodeToString(sum[:])
}

func decodeCopilotPricingState(raw string) (*CopilotPricingState, error) {
	state := &CopilotPricingState{}
	if raw != "" {
		if err := common.UnmarshalJsonStr(raw, state); err != nil {
			return nil, common.NewMessage("GitHub Copilot pricing verification data is invalid")
		}
	}
	if state.Sources == nil {
		state.Sources = make(map[int]CopilotPricingSource)
	}
	if state.Approvals == nil {
		state.Approvals = make(map[string]CopilotPricingReference)
	}
	return state, nil
}

// The cached snapshot is immutable; writers publish only after the DB commit.
func currentCopilotPricingState() (*CopilotPricingState, error) {
	common.OptionMapRWMutex.RLock()
	raw := common.OptionMap[CopilotPricingStateKey]
	common.OptionMapRWMutex.RUnlock()
	copilotPricingCache.Lock()
	defer copilotPricingCache.Unlock()
	if copilotPricingCache.state == nil || copilotPricingCache.raw != raw {
		copilotPricingCache.raw = raw
		copilotPricingCache.state, copilotPricingCache.err = decodeCopilotPricingState(raw)
	}
	return copilotPricingCache.state, copilotPricingCache.err
}

func GetCopilotPricingState() (*CopilotPricingState, error) {
	common.OptionMapRWMutex.RLock()
	raw := common.OptionMap[CopilotPricingStateKey]
	common.OptionMapRWMutex.RUnlock()
	return decodeCopilotPricingState(raw)
}

func (source CopilotPricingSource) CheckFresh(now int64) error {
	if source.LastSuccessAt <= 0 {
		return common.NewMessage("GitHub Copilot pricing has not been verified")
	}
	if now < source.LastSuccessAt || now-source.LastSuccessAt >= CopilotPricingMaxAge {
		return common.NewMessage("GitHub Copilot pricing verification has expired")
	}
	return nil
}

func (source CopilotPricingSource) CheckReference(reference CopilotPricingReference, now int64) error {
	if err := source.CheckFresh(now); err != nil {
		return err
	}
	expression := source.Expressions[reference.Model]
	if expression == "" {
		return common.NewMessage("GitHub Copilot has no verified published price for this model")
	}
	if reference.Version != CopilotPricingVersion(reference.Model, expression) {
		return common.NewMessage("GitHub Copilot pricing changed or needs review")
	}
	return nil
}

func CheckCopilotPricing(channelID int, billingModel, upstreamModel string, now int64) error {
	state, err := currentCopilotPricingState()
	if err != nil {
		return err
	}
	source := state.Sources[channelID]
	if err := source.CheckFresh(now); err != nil {
		return err
	}
	reference, approved := state.Approvals[billingModel]
	if !approved || (upstreamModel != "" && reference.Model != upstreamModel) {
		return common.NewMessage("GitHub Copilot pricing changed or needs review")
	}
	return source.CheckReference(reference, now)
}

func readCopilotPricingStateForUpdate(tx *gorm.DB) (*CopilotPricingState, error) {
	row := Option{Key: CopilotPricingStateKey, Value: "{}"}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return nil, err
	}
	if err := lockForUpdate(tx).Where(commonKeyCol+" = ?", CopilotPricingStateKey).First(&row).Error; err != nil {
		return nil, err
	}
	return decodeCopilotPricingState(row.Value)
}

func writeCopilotPricingState(tx *gorm.DB, state *CopilotPricingState) (string, error) {
	encoded, err := common.Marshal(state)
	if err != nil {
		return "", err
	}
	if err := tx.Model(&Option{}).Where(commonKeyCol+" = ?", CopilotPricingStateKey).Update("value", string(encoded)).Error; err != nil {
		return "", err
	}
	return string(encoded), nil
}

// Failed refreshes record their error without extending or replacing verified prices.
func RecordCopilotPricingCheck(ctx context.Context, channelID int, version string, expressions map[string]string, checkErr error, started time.Time) error {
	modelPricingMutationMu.Lock()
	defer modelPricingMutationMu.Unlock()
	var committed string
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		state, err := readCopilotPricingStateForUpdate(tx)
		if err != nil {
			return err
		}
		source := state.Sources[channelID]
		if source.StartedAt >= started.UnixNano() {
			return errors.New("a newer GitHub Copilot pricing check has already completed")
		}
		source.StartedAt, source.LastAttemptAt = started.UnixNano(), started.Unix()
		if checkErr != nil {
			source.Error = checkErr.Error()
		} else {
			if version == "" || len(expressions) == 0 {
				return common.NewMessage("GitHub Copilot pricing verification data is invalid")
			}
			source.Version, source.LastSuccessAt, source.Error = version, started.Unix(), ""
			source.Expressions = expressions
		}
		state.Sources[channelID] = source
		committed, err = writeCopilotPricingState(tx, state)
		return err
	})
	if err != nil {
		return fmt.Errorf("save GitHub Copilot pricing check: %w", err)
	}
	if err := updateOptionMap(CopilotPricingStateKey, committed); err != nil {
		return err
	}
	BumpSyncTopic(SyncTopicOptions)
	return nil
}
