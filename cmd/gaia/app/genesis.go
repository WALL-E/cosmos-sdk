package app

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/server"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/auth"
	"github.com/cosmos/cosmos-sdk/x/gov"
	"github.com/cosmos/cosmos-sdk/x/stake"
	stakeTypes "github.com/cosmos/cosmos-sdk/x/stake/types"

	"github.com/spf13/pflag"
)

var (
	// bonded tokens given to genesis validators/accounts
	freeFermionVal  = int64(100)
	freeFermionsAcc = sdk.NewInt(50)
)

// State to Unmarshal
type GenesisState struct {
	Accounts  []GenesisAccount   `json:"accounts"`
	StakeData stake.GenesisState `json:"stake"`
	GovData   gov.GenesisState   `json:"gov"`
}

// GenesisAccount doesn't need pubkey or sequence
type GenesisAccount struct {
	Address sdk.AccAddress `json:"address"`
	Coins   sdk.Coins      `json:"coins"`
}

func NewGenesisAccount(acc *auth.BaseAccount) GenesisAccount {
	return GenesisAccount{
		Address: acc.Address,
		Coins:   acc.Coins,
	}
}

func NewGenesisAccountI(acc auth.Account) GenesisAccount {
	return GenesisAccount{
		Address: acc.GetAddress(),
		Coins:   acc.GetCoins(),
	}
}

// convert GenesisAccount to auth.BaseAccount
func (ga *GenesisAccount) ToAccount() (acc *auth.BaseAccount) {
	return &auth.BaseAccount{
		Address: ga.Address,
		Coins:   ga.Coins.Sort(),
	}
}

// get app init parameters for server init command
func GaiaAppInit() server.AppInit {
	fsAppGenState := pflag.NewFlagSet("", pflag.ContinueOnError)
	fsAppGenTx := pflag.NewFlagSet("", pflag.ContinueOnError)
	fsAppGenTx.String(server.FlagName, "", "validator moniker, required")
	fsAppGenTx.String(server.FlagClientHome, DefaultCLIHome,
		"home directory for the client, used for key generation")
	fsAppGenTx.Bool(server.FlagOWK, false, "overwrite the accounts created")

	return server.AppInit{
		FlagsAppGenState: fsAppGenState,
		FlagsAppGenTx:    fsAppGenTx,
		AppGenState:      GaiaAppGenStateJSON,
		AppGenTx:         server.SimpleAppGenTx,
	}
}

// simple genesis tx
type GaiaGenTx struct {
	Name    string         `json:"name"`
	Address sdk.AccAddress `json:"address"`
	PubKey  string         `json:"pub_key"`
}

// Create the core parameters for genesis initialization for gaia
// note that the pubkey input is this machines pubkey
func GaiaAppGenState(cdc *codec.Codec, appGenTxs []auth.StdTx) (genesisState GenesisState, err error) {

	if len(appGenTxs) == 0 {
		err = errors.New("must provide at least genesis transaction")
		return
	}

	// start with the default staking genesis state
	stakeData := stake.DefaultGenesisState()
	genaccs := make([]GenesisAccount, len(appGenTxs))

	for i, genTx := range appGenTxs {
		msgs := genTx.GetMsgs()
		if len(msgs) == 0 {
			err = errors.New("must provide at least genesis message")
			return
		}
		msg := msgs[0].(stake.MsgCreateValidator)

		// create the genesis account, give'm few steaks and a buncha token with there name
		genaccs[i] = genesisAccountFromMsgCreateValidator(msg)
		stakeData.Pool.LooseTokens = stakeData.Pool.LooseTokens.Add(sdk.NewDecFromInt(freeFermionsAcc)) // increase the supply

		// add the validator
		if len(msg.Description.Moniker) > 0 {
			stakeData = addValidatorToStakeData(msg, stakeData)
		}

	}

	// create the final app state
	genesisState = GenesisState{
		Accounts:  genaccs,
		StakeData: stakeData,
		GovData:   gov.DefaultGenesisState(),
	}
	return
}

func addValidatorToStakeData(msg stake.MsgCreateValidator, stakeData stake.GenesisState) stake.GenesisState {
	validator := stake.NewValidator(
		sdk.ValAddress(msg.ValidatorAddr), msg.PubKey, msg.Description,
	)

	stakeData.Pool.LooseTokens = stakeData.Pool.LooseTokens.Add(sdk.NewDec(freeFermionVal)) // increase the supply

	// add some new shares to the validator
	var issuedDelShares sdk.Dec
	validator, stakeData.Pool, issuedDelShares = validator.AddTokensFromDel(stakeData.Pool, sdk.NewInt(freeFermionVal))
	stakeData.Validators = append(stakeData.Validators, validator)

	// create the self-delegation from the issuedDelShares
	delegation := stake.Delegation{
		DelegatorAddr: sdk.AccAddress(validator.OperatorAddr),
		ValidatorAddr: validator.OperatorAddr,
		Shares:        issuedDelShares,
		Height:        0,
	}

	stakeData.Bonds = append(stakeData.Bonds, delegation)
	return stakeData
}

func genesisAccountFromMsgCreateValidator(msg stake.MsgCreateValidator) GenesisAccount {
	accAuth := auth.NewBaseAccountWithAddress(sdk.AccAddress(msg.ValidatorAddr))
	accAuth.Coins = []sdk.Coin{msg.Delegation}
	return NewGenesisAccount(&accAuth)
}

// GaiaValidateGenesisState ensures that the genesis state obeys the expected invariants
// TODO: No validators are both bonded and jailed (#2088)
// TODO: Error if there is a duplicate validator (#1708)
// TODO: Ensure all state machine parameters are in genesis (#1704)
func GaiaValidateGenesisState(genesisState GenesisState) (err error) {
	err = validateGenesisStateAccounts(genesisState.Accounts)
	if err != nil {
		return
	}
	err = validateGenesisStateValidators(genesisState.StakeData.Validators)
	if err != nil {
		return
	}
	return
}

func validateGenesisStateValidators(validators []stakeTypes.Validator) (err error) {
	addrMap := make(map[string]bool, len(validators))
	for i := 0; i < len(validators); i++ {
		val := validators[i]
		strKey := string(val.ConsPubKey.Bytes())
		if _, ok := addrMap[strKey]; ok {
			return fmt.Errorf("Duplicate validator in genesis state: moniker %v, Address %v", val.Description.Moniker, val.ConsAddress())
		}
		if val.Jailed && val.Status == sdk.Bonded {
			return fmt.Errorf("Validator is bonded and jailed in genesis state: moniker %v, Address %v", val.Description.Moniker, val.ConsAddress())
		}
		addrMap[strKey] = true
	}
	return
}

// Ensures that there are no duplicate accounts in the genesis state,
func validateGenesisStateAccounts(accs []GenesisAccount) (err error) {
	addrMap := make(map[string]bool, len(accs))
	for i := 0; i < len(accs); i++ {
		acc := accs[i]
		strAddr := string(acc.Address)
		if _, ok := addrMap[strAddr]; ok {
			return fmt.Errorf("Duplicate account in genesis state: Address %v", acc.Address)
		}
		addrMap[strAddr] = true
	}
	return
}

// GaiaAppGenState but with JSON
func GaiaAppGenStateJSON(cdc *codec.Codec, appGenTxs []auth.StdTx) (appState json.RawMessage, err error) {
	// create the final app state
	genesisState, err := GaiaAppGenState(cdc, appGenTxs)
	if err != nil {
		return nil, err
	}
	appState, err = codec.MarshalJSONIndent(cdc, genesisState)
	return
}
