package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/cross-space-official/common/businesserror"
	"github.com/cross-space-official/common/logger"
	"github.com/cross-space-official/kaboom-service/common"
	"github.com/cross-space-official/kaboom-service/core"
	"github.com/cross-space-official/kaboom-service/model"
	"github.com/cross-space-official/kaboom-service/repository"
	"github.com/cross-space-official/kaboom-service/service/mpc"
	"github.com/cross-space-official/kaboom-service/service/provider/evm"
	utils2 "github.com/cross-space-official/kaboom-service/utils"
	"github.com/ethereum/go-ethereum/accounts/abi"
	common2 "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

type evmTradeService struct {
	gethService     evm.GethService
	userService     UserService
	cutonomyService mpc.CustonomyService
	pollingManager  MpcPollingManager
	kaboomRouterAbi abi.ABI

	assetRepository        repository.AssetRepository
	tokenBalanceRepository repository.TokenBalanceRepository
	logRepository          repository.EventLogRepository
}

func (a *evmTradeService) RefreshTokenBalancesByUser(ctx context.Context, user model.User) ([]*model.TokenBalance, businesserror.XSpaceBusinessError) {
	balances, err := a.tokenBalanceRepository.RetrieveTokenPositiveBalancesByUserID(ctx, user.ID, user.DefaultChainID)
	if err != nil {
		return nil, err
	}

	var wg sync.WaitGroup

	for i := range balances {
		wg.Add(1)

		go func(balance *model.TokenBalance) {
			defer wg.Done()

			val, err := a.gethService.GetTokenBalance(
				ctx, balance.DexPair.ChainID,
				user.GetWalletAddress(balance.DexPair.ChainID),
				balance.DexPair.GetToken().ContractAddress,
			)
			if err != nil {
				logger.GetLoggerEntry(ctx).
					WithField("user_id", user.ID).
					WithField("chain_id", user.DefaultChainID).
					WithField("token_id", balance.DexPair.GetToken().ID).
					Error("Failed to get token balance: ", err)
				return
			}

			balance.BalanceInWei = model.NewBigInt(*val)
		}(balances[i])
	}

	wg.Wait()

	err = a.tokenBalanceRepository.UpdateTokenBalances(ctx, balances)
	if err != nil {
		logger.GetLoggerEntry(ctx).
			WithField("user_id", user.ID).
			WithField("chain_id", user.DefaultChainID).
			Error("Failed to update token balances: ", err)
	}

	return balances, nil
}

func (a *evmTradeService) ComposeTransactionApprovePairByID(ctx context.Context, userID, pairID string, amountInWei *big.Int) (*WalletSignPayload, businesserror.XSpaceBusinessError) {
	sellValueInWei := amountInWei
	if sellValueInWei == nil {
		return nil, common.NewRuntimeError(errors.New(common.InvalidAmountFailure))
	}

	pair, err := a.assetRepository.RetrievePairByPairID(ctx, pairID)
	if err != nil {
		return nil, err
	}

	routerAddress, err := a.gethService.GetKaboomRouterAddress(pair.ChainID)
	if err != nil {
		return nil, err
	}

	user, err := a.userService.RetrieveUserByID(ctx, userID)
	if err != nil {
		return nil, err
	}

	if !user.HasWalletAddress() {
		return nil, common.NewRuntimeError(errors.New(common.NoBoundWalletFailure))
	}

	userWalletAddress := user.GetWalletAddress(pair.ChainID)

	allowance, err := a.gethService.GetTokenApproveAmount(ctx, pair.ChainID, pair.GetToken().ContractAddress, userWalletAddress, routerAddress)
	if err != nil {
		return nil, err
	}

	if allowance.Cmp(sellValueInWei) >= 0 {
		return nil, nil
	}

	return &WalletSignPayload{
		ToAddress:  pair.GetToken().ContractAddress,
		ChainID:    pair.ChainID,
		Method:     "approve",
		ValueInWei: "0",
		CallData: map[string]interface{}{
			"spender": routerAddress,
			"amount":  "0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
		},
	}, nil
}

func (a *evmTradeService) ComposeTransactionSellPairByID(ctx context.Context, userID, pairID string, amountInWei, minimalOutAmountInWei *big.Int) (*WalletSignPayload, businesserror.XSpaceBusinessError) {
	sellValueInWei := amountInWei
	if sellValueInWei == nil {
		return nil, common.NewRuntimeError(errors.New(common.InvalidAmountFailure))
	}

	pair, err := a.assetRepository.RetrievePairByPairID(ctx, pairID)
	if err != nil {
		return nil, err
	}

	routerAddress, err := a.gethService.GetKaboomRouterAddress(pair.ChainID)
	if err != nil {
		return nil, err
	}

	user, err := a.userService.RetrieveUserByID(ctx, userID)
	if err != nil {
		return nil, err
	}

	if !user.HasWalletAddress() {
		return nil, common.NewRuntimeError(errors.New(common.NoBoundWalletFailure))
	}

	userSettings := user.GetUserSettingsByChainID(pair.ChainID)
	userWalletAddress := user.GetWalletAddress(pair.ChainID)

	if minimalOutAmountInWei == nil {
		expectETHAmountOutWei := decimal.NewFromBigInt(pair.GetSellAmountOut(sellValueInWei), 0)
		expectETHAmountOutWeiWithSlippage :=
			expectETHAmountOutWei.
				Div(decimal.NewFromInt(model.PercentageBase)).
				Mul(decimal.NewFromInt(model.PercentageBase - int64(userSettings.GetMaxSlippage())))
		minimalOutAmountInWei = expectETHAmountOutWeiWithSlippage.BigInt()
	}

	return &WalletSignPayload{
		ToAddress:  routerAddress,
		ChainID:    pair.ChainID,
		Method:     "swapExactTokensForETHSupportingFeeOnTransferTokens",
		ValueInWei: "0",
		CallData: map[string]interface{}{
			"requestId":    uuid.NewString(),
			"amountIn":     sellValueInWei.String(),
			"amountOutMin": minimalOutAmountInWei.String(),
			"tokenAddress": pair.GetToken().ContractAddress,
			"to":           userWalletAddress,
			"deadline":     time.Now().Add(10 * time.Minute).Unix(),
		},
	}, nil
}

func (a *evmTradeService) ComposeTransactionBuyPairByID(ctx context.Context, userID, pairID string, amountInWei, minimalOutAmountInWei *big.Int) (*WalletSignPayload, businesserror.XSpaceBusinessError) {
	pair, err := a.assetRepository.RetrievePairByPairID(ctx, pairID)
	if err != nil {
		return nil, err
	}

	routerAddress, err := a.gethService.GetKaboomRouterAddress(pair.ChainID)
	if err != nil {
		return nil, err
	}

	user, err := a.userService.RetrieveUserByID(ctx, userID)
	if err != nil {
		return nil, err
	}

	if !user.HasWalletAddress() {
		return nil, common.NewRuntimeError(errors.New(common.NoBoundWalletFailure))
	}

	userSettings := user.GetUserSettingsByChainID(pair.ChainID)
	userWalletAddress := user.GetWalletAddress(pair.ChainID)

	buyValueInWei := amountInWei
	if buyValueInWei == nil {
		buyValueInWei = userSettings.GetBoomAmountInWei(pair.ChainID).BigInt()
	}

	if minimalOutAmountInWei == nil {
		expectTokenAmountOutWei := decimal.NewFromBigInt(pair.GetBuyAmountOut(buyValueInWei), 0)
		expectTokenAmountOutWeiWithSlippage :=
			expectTokenAmountOutWei.
				Div(decimal.NewFromInt(model.PercentageBase)).
				Mul(decimal.NewFromInt(model.PercentageBase - int64(userSettings.GetMaxSlippage())))
		minimalOutAmountInWei = expectTokenAmountOutWeiWithSlippage.BigInt()
	}

	return &WalletSignPayload{
		ToAddress:  routerAddress,
		ChainID:    pair.ChainID,
		Method:     "swapExactETHForTokensSupportingFeeOnTransferTokens",
		ValueInWei: buyValueInWei.String(),
		CallData: map[string]interface{}{
			"requestId":    uuid.NewString(),
			"amountOutMin": minimalOutAmountInWei.String(),
			"tokenAddress": pair.GetToken().ContractAddress,
			"to":           userWalletAddress,
			"deadline":     time.Now().Add(10 * time.Minute).Unix(),
		},
	}, nil
}

func (a *evmTradeService) GetNativeTokenBalanceByUserID(c context.Context, chainID, userID string) (*big.Int, businesserror.XSpaceBusinessError) {
	user, err := a.userService.RetrieveUserByID(c, userID)
	if err != nil {
		return nil, err
	}

	if !user.HasWalletAddress() {
		return big.NewInt(0), nil
	}

	balance, err := a.gethService.GetNativeTokenBalance(c, chainID, user.GetWalletAddress(chainID))
	if err != nil {
		return nil, err
	}

	return balance, nil
}

func (a *evmTradeService) ApprovePairByIDSync(ctx context.Context, userID, pairID, jwt string, amountInWei *big.Int) businesserror.XSpaceBusinessError {
	pair, err := a.assetRepository.RetrievePairByPairID(ctx, pairID)
	if err != nil {
		return err
	}

	user, err := a.userService.RetrieveUserByID(ctx, userID)
	if err != nil {
		return err
	}

	if user.UserType != model.UserTypeMpc {
		return common.NewRuntimeError(errors.New(common.InvalidWalletType))
	}

	userWalletAddress := user.GetWalletAddress(pair.ChainID)

	if !user.HasWalletAddress() {
		return common.NewRuntimeError(errors.New(common.NoBoundWalletFailure))
	}

	data, err := a.packApproveData(pair.ChainID, amountInWei)
	if err != nil {
		return err
	}

	gasEst, err := a.gethService.EstimateGas(ctx, pair.ChainID, userWalletAddress, pair.GetToken().ContractAddress, data, big.NewInt(0))
	if err != nil {
		return err
	}

	nonce, err := a.getNextNonce(ctx, pair.ChainID, user)
	if err != nil {
		return err
	}

	gasPriceInWei, err := a.gethService.GetGasPrice(ctx, pair.ChainID)
	if err != nil {
		return err
	}

	chainID, _ := strconv.Atoi(pair.ChainID)
	txn := mpc.Transaction{
		From:     userWalletAddress,
		To:       pair.GetToken().ContractAddress,
		ChainID:  chainID,
		Data:     hexutil.Encode(data),
		Nonce:    nonce,
		GasPrice: hexutil.EncodeUint64(gasPriceInWei.Uint64()),
		GasLimit: hexutil.EncodeUint64(gasEst),
		Type:     0,
		Value:    fmt.Sprintf("0x%x", big.NewInt(0)),
	}

	id, err := a.cutonomyService.SubmitTransaction(ctx, uuid.New().String(), jwt, txn)
	if err != nil {
		return err
	}

	txnHash, err := a.cutonomyService.PollTransactionByRequestID(ctx, id, jwt)
	if err != nil {
		return err
	}

	requestLog := model.NewRequestLog(id, id, userID, model.RequestBusinessTypeApprove, amountInWei.String(), "", utils2.Ref(pair.GetToken().ID), txn)
	_ = a.logRepository.CreateRequestLog(ctx, requestLog)

	for i := 0; i < 5; i++ {
		receipt, err := a.gethService.GetTransactionReceipt(ctx, pair.ChainID, txnHash)
		if err != nil {
			return err
		}

		if receipt != nil {
			if receipt.Status == 1 {
				_ = a.logRepository.UpdateRequestLogByReturnedID(ctx, id, model.RequestStatusConfirmed, txnHash, "")
				return nil
			} else {
				_ = a.logRepository.UpdateRequestLogByReturnedID(ctx, id, model.RequestStatusFailed, "", "reverted transaction")
				return common.NewRuntimeError(errors.New("transaction reverted"))
			}
		}

		time.Sleep(5 * time.Second)
	}

	_ = a.logRepository.UpdateRequestLogByReturnedID(ctx, id, model.RequestStatusFailed, "", "no receipt from block")
	return common.NewRuntimeError(errors.New("transaction reverted"))
}

func (a *evmTradeService) PreflightBuyPairByID(ctx context.Context, userID, pairID string, amountInWei *big.Int) (bool, *mpc.Transaction, businesserror.XSpaceBusinessError) {
	pair, err := a.assetRepository.RetrievePairByPairID(ctx, pairID)
	if err != nil {
		return false, nil, err
	}

	routerAddress, err := a.gethService.GetKaboomRouterAddress(pair.ChainID)
	if err != nil {
		return false, nil, err
	}

	user, err := a.userService.RetrieveUserByID(ctx, userID)
	if err != nil {
		return false, nil, err
	}

	if !user.HasWalletAddress() {
		return false, nil, common.NewRuntimeError(errors.New(common.NoBoundWalletFailure))
	}

	userSettings := user.GetUserSettingsByChainID(pair.ChainID)
	userWalletAddress := user.GetWalletAddress(pair.ChainID)

	requestID := uuid.New().String()
	buyValueInWei := amountInWei
	if buyValueInWei == nil {
		buyValueInWei = userSettings.GetBoomAmountInWei(pair.ChainID).BigInt()
	}
	data, err := a.packBuyData(requestID, pair.GetToken().ContractAddress, pair, user, buyValueInWei, nil)
	if err != nil {
		return false, nil, err
	}

	gasEst, err := a.gethService.EstimateGas(ctx, pair.ChainID, userWalletAddress, routerAddress, data, buyValueInWei)
	if err != nil {
		return false, nil, err
	}

	nonce, err := a.getNextNonce(ctx, pair.ChainID, user)
	if err != nil {
		return false, nil, err
	}

	gasPriceInWei, err := a.gethService.GetGasPrice(ctx, pair.ChainID)
	if err != nil {
		return false, nil, err
	}

	chainID, _ := strconv.Atoi(pair.ChainID)
	prefilghtTxn := mpc.Transaction{
		From:     userWalletAddress,
		To:       routerAddress,
		ChainID:  chainID,
		Data:     hexutil.Encode(data),
		Nonce:    nonce,
		GasPrice: gasPriceInWei.String(),
		GasLimit: strconv.FormatUint(gasEst, 10),
		Type:     0,
		Value:    buyValueInWei.String(),
	}

	if userSettings.IsTransactionConfirmationEnabled() {
		gasPrice := decimal.NewFromBigInt(gasPriceInWei, 0)
		gasLimit := decimal.NewFromUint64(gasEst)

		if userSettings.GasTransactionConfirmationThresholdInWei.Cmp(gasPrice.Mul(gasLimit)) <= 0 {
			return true, &prefilghtTxn, nil
		}
	}

	return false, &prefilghtTxn, nil
}

func (a *evmTradeService) PreviewBuyPairByID(ctx context.Context, pairID string, inAmountInWei, outAmountInWei *big.Int) (*model.DexPair, *big.Int, businesserror.XSpaceBusinessError) {
	pair, err := a.assetRepository.RetrievePairByPairID(ctx, pairID)
	if err != nil {
		return nil, nil, err
	}

	if inAmountInWei != nil {
		expectTokenAmountOutWei := decimal.NewFromBigInt(pair.GetBuyAmountOut(inAmountInWei), 0)
		return pair, expectTokenAmountOutWei.BigInt(), nil
	}

	expectEthAmountInWei := decimal.NewFromBigInt(pair.GetBuyAmountIn(outAmountInWei), 0)
	return pair, expectEthAmountInWei.BigInt(), nil
}

func (a *evmTradeService) PreviewSellPairByID(ctx context.Context, pairID string, inAmountInWei, outAmountInWei *big.Int) (*model.DexPair, *big.Int, businesserror.XSpaceBusinessError) {
	pair, err := a.assetRepository.RetrievePairByPairID(ctx, pairID)
	if err != nil {
		return nil, nil, err
	}

	if inAmountInWei != nil {
		expectEthAmountOutWei := decimal.NewFromBigInt(pair.GetSellAmountOut(inAmountInWei), 0)
		return pair, expectEthAmountOutWei.BigInt(), nil
	}

	expectTokenAmountInWei := decimal.NewFromBigInt(pair.GetSellAmountIn(outAmountInWei), 0)
	return pair, expectTokenAmountInWei.BigInt(), nil
}

func (a *evmTradeService) WithdrawNativeTokenByUserID(ctx context.Context, userID, jwt, chainIDStr, toAddress string, amountInWei *big.Int, clientIp string) businesserror.XSpaceBusinessError {
	if amountInWei == nil {
		return nil
	}

	user, err := a.userService.RetrieveUserByID(ctx, userID)
	if err != nil {
		return err
	}

	if !user.HasWalletAddress() {
		return common.NewRuntimeError(errors.New(common.NoBoundWalletFailure))
	}

	if user.UserType != model.UserTypeMpc {
		return common.NewRuntimeError(errors.New(common.InvalidWalletType))
	}

	userWalletAddress := user.GetDefaultWalletAddress()

	gasEst, err := a.gethService.EstimateGas(ctx, chainIDStr, userWalletAddress, toAddress, []byte("0x0"), amountInWei)
	if err != nil {
		return err
	}

	nonce, err := a.getNextNonce(ctx, chainIDStr, user)
	if err != nil {
		return err
	}

	gasPriceInWei, err := a.gethService.GetGasPrice(ctx, chainIDStr)
	if err != nil {
		return err
	}

	chainID, _ := strconv.Atoi(chainIDStr)
	txn := mpc.Transaction{
		From:     userWalletAddress,
		To:       toAddress,
		ChainID:  chainID,
		Data:     hexutil.EncodeUint64(0),
		Nonce:    nonce,
		GasPrice: hexutil.EncodeUint64(gasPriceInWei.Uint64()),
		GasLimit: hexutil.EncodeUint64(gasEst),
		Type:     0,
		Value:    fmt.Sprintf("0x%x", amountInWei),
	}

	returnedID, err := a.cutonomyService.SubmitTransaction(ctx, uuid.New().String(), jwt, txn)
	if err != nil {
		return err
	}

	requestLog := model.NewRequestLog(returnedID, returnedID, userID, model.RequestBusinessTypeTransferOut, amountInWei.String(), clientIp, nil, txn)
	_ = a.logRepository.CreateRequestLog(ctx, requestLog)

	go a.pollingManager.StartPolling(ctx, user.ID, jwt)

	return nil
}

func (a *evmTradeService) SellPairByID(ctx context.Context, userID, pairID, jwt string, amountInWei, minimalOutAmountInWei *big.Int, clientIp string) businesserror.XSpaceBusinessError {
	sellValueInWei := amountInWei
	if sellValueInWei == nil {
		return common.NewRuntimeError(errors.New(common.InvalidAmountFailure))
	}

	pair, err := a.assetRepository.RetrievePairByPairID(ctx, pairID)
	if err != nil {
		return err
	}

	routerAddress, err := a.gethService.GetKaboomRouterAddress(pair.ChainID)
	if err != nil {
		return err
	}

	user, err := a.userService.RetrieveUserByID(ctx, userID)
	if err != nil {
		return err
	}

	if user.UserType != model.UserTypeMpc {
		return common.NewRuntimeError(errors.New(common.InvalidWalletType))
	}

	if !user.HasWalletAddress() {
		return common.NewRuntimeError(errors.New(common.NoBoundWalletFailure))
	}
	userWalletAddress := user.GetWalletAddress(pair.ChainID)

	allowance, err := a.gethService.GetTokenApproveAmount(ctx, pair.ChainID, pair.GetToken().ContractAddress, userWalletAddress, routerAddress)
	if err != nil {
		return err
	}

	if allowance.Cmp(sellValueInWei) >= 0 {
		return a.submitSellTransaction(ctx, user, pair, jwt, clientIp, sellValueInWei, minimalOutAmountInWei)
	}

	err = a.pilotRunSellTransaction(ctx, user, pair, sellValueInWei)
	if err != nil {
		return err
	}

	go func() {
		err = a.ApprovePairByIDSync(ctx, userID, pairID, jwt, sellValueInWei)
		if err != nil {
			logger.GetLoggerEntry(ctx).
				WithField("pair_id", pairID).
				WithField("user_id", userID).
				WithField("message", err.Message()).
				Warn("Failed to approve pair: ", err)
			return
		}

		err = a.submitSellTransaction(ctx, user, pair, jwt, clientIp, sellValueInWei, minimalOutAmountInWei)
		if err != nil {
			logger.GetLoggerEntry(ctx).
				WithField("pair_id", pairID).
				WithField("user_id", userID).
				WithField("message", err.Message()).
				Warn("Failed to sell pair: ", err)
			return
		}

	}()

	return nil
}

func (a *evmTradeService) BuyPairByID(ctx context.Context, userID, pairID, jwt string, amountInWei, minimalOutAmountInWei, suggestedGasPrice, suggestedGasLimit *big.Int, clientIp string) businesserror.XSpaceBusinessError {
	pair, err := a.assetRepository.RetrievePairByPairID(ctx, pairID)
	if err != nil {
		return err
	}

	routerAddress, err := a.gethService.GetKaboomRouterAddress(pair.ChainID)
	if err != nil {
		return err
	}

	user, err := a.userService.RetrieveUserByID(ctx, userID)
	if err != nil {
		return err
	}

	if user.UserType != model.UserTypeMpc {
		return common.NewRuntimeError(errors.New(common.InvalidWalletType))
	}

	if !user.HasWalletAddress() {
		return common.NewRuntimeError(errors.New(common.NoBoundWalletFailure))
	}

	userSettings := user.GetUserSettingsByChainID(pair.ChainID)
	userWalletAddress := user.GetWalletAddress(pair.ChainID)

	requestID := uuid.New().String()
	buyValueInWei := amountInWei
	if buyValueInWei == nil {
		buyValueInWei = userSettings.GetBoomAmountInWei(pair.ChainID).BigInt()
	}
	data, err := a.packBuyData(requestID, pair.GetToken().ContractAddress, pair, user, buyValueInWei, minimalOutAmountInWei)
	if err != nil {
		return err
	}

	gasEst, err := a.gethService.EstimateGas(ctx, pair.ChainID, userWalletAddress, routerAddress, data, buyValueInWei)
	if err != nil {
		return err
	}

	nonce, err := a.getNextNonce(ctx, pair.ChainID, user)
	if err != nil {
		return err
	}

	gasPriceInWei, err := a.gethService.GetGasPrice(ctx, pair.ChainID)
	if err != nil {
		return err
	}

	chainID, _ := strconv.Atoi(pair.ChainID)
	txn := mpc.Transaction{
		From:     userWalletAddress,
		To:       routerAddress,
		ChainID:  chainID,
		Data:     hexutil.Encode(data),
		Nonce:    nonce,
		GasPrice: hexutil.EncodeUint64(gasPriceInWei.Uint64()),
		GasLimit: hexutil.EncodeUint64(gasEst),
		Type:     0,
		Value:    fmt.Sprintf("0x%x", buyValueInWei),
	}

	if suggestedGasPrice != nil {
		txn.GasPrice = hexutil.EncodeUint64(suggestedGasPrice.Uint64())
	}

	if suggestedGasLimit != nil {
		txn.GasLimit = hexutil.EncodeUint64(suggestedGasLimit.Uint64())
	}

	returnedID, err := a.cutonomyService.SubmitTransaction(ctx, requestID, jwt, txn)
	if err != nil {
		return err
	}

	requestLog := model.NewRequestLog(requestID, returnedID, userID, model.RequestBusinessTypeBuyToken, buyValueInWei.String(), clientIp, utils2.Ref(pair.GetToken().ID), txn)
	_ = a.logRepository.CreateRequestLog(ctx, requestLog)

	go a.pollingManager.StartPolling(ctx, user.ID, jwt)

	return err
}

func (a *evmTradeService) packApproveData(chainID string, amountInWei *big.Int) ([]byte, businesserror.XSpaceBusinessError) {
	routerAddress, bizErr := a.gethService.GetKaboomRouterAddress(chainID)
	if bizErr != nil {
		return nil, bizErr
	}

	erc20, err := core.TokenMetaData.GetAbi()
	if err != nil {
		return nil, common.NewRuntimeError(err)
	}

	raw, err := erc20.Pack(
		"approve",
		common2.HexToAddress(routerAddress),
		amountInWei,
	)
	if err != nil {
		return nil, common.NewRuntimeError(err)
	}
	return raw, nil
}

func (a *evmTradeService) packBuyData(requestID, tokenAddress string, dexPair *model.DexPair, user *model.User,
	amountInWei, minimalOutAmountInWei *big.Int) ([]byte, businesserror.XSpaceBusinessError) {
	userSettings := user.GetUserSettingsByChainID(dexPair.ChainID)

	if minimalOutAmountInWei == nil {
		expectTokenAmountOutWei := decimal.NewFromBigInt(dexPair.GetBuyAmountOut(amountInWei), 0)
		expectTokenAmountOutWeiWithSlippage :=
			expectTokenAmountOutWei.
				Div(decimal.NewFromInt(model.PercentageBase)).
				Mul(decimal.NewFromInt(model.PercentageBase - int64(userSettings.GetMaxSlippage())))
		minimalOutAmountInWei = expectTokenAmountOutWeiWithSlippage.BigInt()
	}

	ddl := time.Now().Add(10 * time.Minute).UnixMilli()
	raw, err := a.kaboomRouterAbi.Pack(
		"swapExactETHForTokensSupportingFeeOnTransferTokens",
		requestID,
		minimalOutAmountInWei,
		common2.HexToAddress(tokenAddress),
		common2.HexToAddress(user.GetWalletAddress(dexPair.ChainID)),
		big.NewInt(ddl),
	)
	if err != nil {
		return nil, common.NewRuntimeError(err)
	}
	return raw, nil
}

func (a *evmTradeService) packSellData(requestID, tokenAddress string, dexPair *model.DexPair, user *model.User, sellAmountInWei, minimalOutAmountInWei *big.Int) ([]byte, businesserror.XSpaceBusinessError) {
	userSettings := user.GetUserSettingsByChainID(dexPair.ChainID)

	if minimalOutAmountInWei == nil {
		expectETHAmountOutWei := decimal.NewFromBigInt(dexPair.GetSellAmountOut(sellAmountInWei), 0)
		expectETHAmountOutWeiWithSlippage :=
			expectETHAmountOutWei.
				Div(decimal.NewFromInt(model.PercentageBase)).
				Mul(decimal.NewFromInt(model.PercentageBase - int64(userSettings.GetMaxSlippage())))
		minimalOutAmountInWei = expectETHAmountOutWeiWithSlippage.BigInt()
	}

	ddl := time.Now().Add(10 * time.Minute).UnixMilli()
	raw, err := a.kaboomRouterAbi.Pack(
		"swapExactTokensForETHSupportingFeeOnTransferTokens",
		requestID,
		sellAmountInWei,
		minimalOutAmountInWei,
		common2.HexToAddress(tokenAddress),
		common2.HexToAddress(user.GetWalletAddress(dexPair.ChainID)),
		big.NewInt(ddl),
	)
	if err != nil {
		return nil, common.NewRuntimeError(err)
	}
	return raw, nil
}

func (a *evmTradeService) pilotRunSellTransaction(
	ctx context.Context,
	user *model.User,
	pair *model.DexPair,
	sellValueInWei *big.Int,
) businesserror.XSpaceBusinessError {
	data, err := a.packApproveData(pair.ChainID, sellValueInWei)
	if err != nil {
		return err
	}

	_, err = a.gethService.EstimateGas(ctx, pair.ChainID, user.GetWalletAddress(pair.ChainID), pair.GetToken().ContractAddress, data, big.NewInt(0))
	if err != nil {
		return err
	}

	return err
}

func (a *evmTradeService) submitSellTransaction(
	ctx context.Context,
	user *model.User,
	pair *model.DexPair,
	jwt, clientIp string,
	sellValueInWei *big.Int,
	minimalOutAmountInWei *big.Int,
) businesserror.XSpaceBusinessError {
	routerAddress, err := a.gethService.GetKaboomRouterAddress(pair.ChainID)
	if err != nil {
		return err
	}

	requestID := uuid.New().String()
	data, err := a.packSellData(requestID, pair.GetToken().ContractAddress, pair, user, sellValueInWei, minimalOutAmountInWei)
	if err != nil {
		return err
	}

	userWalletAddress := user.GetWalletAddress(pair.ChainID)
	gasEst, err := a.gethService.EstimateGas(ctx, pair.ChainID, userWalletAddress, routerAddress, data, big.NewInt(0))
	if err != nil {
		return err
	}

	nonce, err := a.getNextNonce(ctx, pair.ChainID, user)
	if err != nil {
		return err
	}

	gasPriceInWei, err := a.gethService.GetGasPrice(ctx, pair.ChainID)
	if err != nil {
		return err
	}

	chainID, _ := strconv.Atoi(pair.ChainID)
	txn := mpc.Transaction{
		From:     userWalletAddress,
		To:       routerAddress,
		ChainID:  chainID,
		Data:     hexutil.Encode(data),
		Nonce:    nonce,
		GasPrice: hexutil.EncodeUint64(gasPriceInWei.Uint64()),
		GasLimit: hexutil.EncodeUint64(gasEst),
		Type:     0,
		Value:    hexutil.EncodeUint64(0),
	}

	returnedID, err := a.cutonomyService.SubmitTransaction(ctx, requestID, jwt, txn)
	if err != nil {
		return err
	}

	requestLog := model.NewRequestLog(requestID, returnedID, user.ID, model.RequestBusinessTypeSellToken, sellValueInWei.String(), clientIp, utils2.Ref(pair.GetToken().ID), txn)
	_ = a.logRepository.CreateRequestLog(ctx, requestLog)

	go a.pollingManager.StartPolling(ctx, user.ID, jwt)

	return nil
}

func (a *evmTradeService) getNextNonce(c context.Context, chainID string, user *model.User) (uint64, businesserror.XSpaceBusinessError) {
	if user == nil {
		return 0, nil
	}

	nonce, err := a.gethService.GetNonce(c, chainID, user.GetWalletAddress(chainID))
	if err != nil {
		return 0, err
	}

	logger.GetLoggerEntry(c).WithField("nonce", nonce).Infof("retrieved nonce for user: %s", user.ID)

	userNonce, _ := a.userService.TryRetrieveUserNextNonceWithDefaultByID(c, chainID, user.ID, nonce)
	if userNonce != nil {
		return userNonce.Nonce, nil
	}

	return nonce, nil
}

func NewEvmTradeService(
	gethService evm.GethService,
	assetRepository repository.AssetRepository,
	tokenBalanceRepository repository.TokenBalanceRepository,
	eventLogRepository repository.EventLogRepository,
) TradeService {
	path, _ := filepath.Abs("./resource/kaboom_router_abi.json")
	file, err := os.ReadFile(path)
	if err != nil {
		panic("Failed to read file")
	}

	kaboomRouterAbi, err := abi.JSON(bytes.NewReader(file))
	if err != nil {
		panic(err)
	}

	cutonomyService := mpc.NewWalletService()

	return &evmTradeService{
		assetRepository:        assetRepository,
		tokenBalanceRepository: tokenBalanceRepository,
		userService:            NewUserService(eventLogRepository),
		kaboomRouterAbi:        kaboomRouterAbi,
		cutonomyService:        cutonomyService,
		gethService:            gethService,
		pollingManager:         NewMpcPollingManager(cutonomyService, eventLogRepository),
		logRepository:          eventLogRepository,
	}
}
