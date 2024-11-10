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
	"github.com/cross-space-official/kaboom-service/service/provider/evm"
	"github.com/ethereum/go-ethereum/accounts/abi"
	common2 "github.com/ethereum/go-ethereum/common"
	"github.com/shopspring/decimal"
	"math/big"
	"os"
	"path/filepath"
	"strings"
)

type (
	dexEvmPairService struct {
		uniSwapV2Abi    abi.ABI
		gethService     evm.GethService
		uploadService   repository.UploadRepository
		assetRepository repository.AssetRepository
		nativeTokenMap  map[string]*model.Token
	}
)

func (d *dexEvmPairService) PublishPairs(ctx context.Context, chainID, pairType string, pairAddresses []string) businesserror.XSpaceBusinessError {
	//TODO implement me
	panic("implement me")
}

func (d *dexEvmPairService) CreatePairFromAddress(ctx context.Context, chainID, pairType, pairAddress string) businesserror.XSpaceBusinessError {
	if pairType != model.PairTypePancakeSwapV2 && pairType != model.PairTypeUniSwapV2 {
		return common.NewRuntimeError(errors.New(common.InvalidPairType))
	}

	client, err := d.gethService.GetClient(chainID)
	if err != nil {
		return err
	}

	instance, basicErr := core.NewUniswapv2pair(common2.HexToAddress(pairAddress), client)
	if err != nil {
		return common.NewRuntimeError(basicErr)
	}

	if _, ok := d.nativeTokenMap[chainID]; !ok {
		chain, err := d.assetRepository.RetrieveChainByID(ctx, chainID)
		if err != nil {
			logger.GetLoggerEntry(ctx).Errorf("Failed to retrieve wnative token for chain: %s", chainID)
			return nil
		}

		d.nativeTokenMap[chainID] = chain.NativeToken
	}
	nativeToken := d.nativeTokenMap[chainID]

	token0, basicErr := instance.Token0(nil)
	if basicErr != nil {
		return common.NewRuntimeError(basicErr)
	}

	token1, basicErr := instance.Token1(nil)
	if basicErr != nil {
		return common.NewRuntimeError(basicErr)
	}

	token0ID := ""
	token1ID := ""
	tokenAddress := ""
	if token0.String() == nativeToken.ContractAddress {
		tokenAddress = token1.String()
		token0ID = nativeToken.ID
	} else {
		tokenAddress = token0.String()
		token1ID = nativeToken.ID
	}

	var reserve struct {
		Reserve0           *big.Int
		Reserve1           *big.Int
		BlockTimestampLast uint32
	}
	reserve, basicErr = instance.GetReserves(nil)
	if basicErr != nil {
		return common.NewRuntimeError(basicErr)
	}

	token := model.Token{
		ChainID:         chainID,
		ContractAddress: tokenAddress,
		IconFileURL:     "",
	}

	pair := &model.DexPair{
		Type:            model.PairType(pairType),
		ChainID:         chainID,
		ContractAddress: pairAddress,
		Token0ID:        token0ID,
		Token1ID:        token1ID,
		Reserve0:        model.NewBigInt(*reserve.Reserve0),
		Reserve1:        model.NewBigInt(*reserve.Reserve1),
		IsPublished:     true,
		ForcePublish:    true,
	}

	err = d.assetRepository.CreatePairWithToken(ctx, &token, pair)
	if err != nil {
		return err
	}

	if pair.Token0ID == token.ID {
		pair.Token0 = token
		pair.Token1 = *nativeToken
	} else {
		pair.Token0 = *nativeToken
		pair.Token1 = token
	}

	err = d.SyncPair(ctx, pair)
	if err != nil {
		return err
	}

	return nil
}

func (d *dexEvmPairService) SyncPair(c context.Context, pair *model.DexPair) businesserror.XSpaceBusinessError {
	totalSupply, burnedSupply, err := d.gethService.GetV2PairSupply(c, *pair)
	if err != nil {
		logger.GetLoggerEntry(c).
			WithField("pair_id", pair.ID).
			Errorf("error getting pair supply, %v", err)
	} else {
		pair.TotalSupply = model.NewBigInt(*totalSupply)
		pair.BurnedSupply = model.NewBigInt(*burnedSupply)

		err = d.assetRepository.UpdateDexPair(c, pair)
		if err != nil {
			logger.GetLoggerEntry(c).Errorf("error updating pair, %v", err)
			return err
		}
	}

	token := pair.GetToken()
	if !token.IsRenounced {
		owner, err := d.gethService.GetTokenOwnerAddress(c, token)
		if err != nil {
			logger.GetLoggerEntry(c).
				WithField("token_id", token.ID).
				WithField("contract_address", token.ContractAddress).
				Errorf("error getting token owner, %v", err)
		} else if strings.EqualFold(owner, common.AddressZero) {
			token.IsRenounced = true
		}
	}

	totalSupply, err = d.gethService.GetTokenTotalSupply(c, token)
	if err != nil {
		logger.GetLoggerEntry(c).
			WithField("token_id", token.ID).
			WithField("contract_address", token.ContractAddress).
			Errorf("error getting token total supply, %v", err)
	} else {
		ethOut := core.GetAmountOut(
			decimal.NewFromBigInt(big.NewInt(1), int32(token.Decimals)),
			decimal.NewFromBigInt(pair.GetTokenReserve(), 0),
			decimal.NewFromBigInt(pair.GetWNativeReserve(), 0))

		token.TotalSupply = model.NewBigInt(*totalSupply)
		token.MarketCapInNative = model.NewBigInt(*big.NewInt(1).
			Div(big.NewInt(1).Mul(totalSupply, ethOut.BigInt()), model.GetChainNativeByID(pair.ChainID).BigInt()))
	}

	if token.Decimals == 0 {
		tokenMetadata, err := d.gethService.GetTokenMetadata(c, token)
		if err == nil && tokenMetadata != nil {
			token.Name = tokenMetadata.Name
			token.Symbol = tokenMetadata.Symbol
			token.Decimals = tokenMetadata.Decimals
		}
	}

	if token.IconFileURL == "" || strings.HasSuffix(token.IconFileURL, "default-token.png") {
		url, err := d.uploadService.CreateFileFromURL(c, token.ID,
			"kaboom",
			fmt.Sprintf("https://dd.dexscreener.com/ds-data/tokens/%s/%s.png",
				model.GetChainNameByID(token.ChainID), token.ContractAddress))
		if err == nil {
			token.IconFileURL = url
		} else {
			token.IconFileURL = fmt.Sprintf("https://dd.dexscreener.com/ds-data/tokens/%s/%s.png",
				model.GetChainNameByID(token.ChainID), token.ContractAddress)
		}
	}

	err = d.assetRepository.UpdateToken(c, &token)
	if err != nil {
		logger.GetLoggerEntry(c).Errorf("error updating token, %v", err)
		return err
	}

	return nil
}

func NewEvmDexPairService(
	gethService evm.GethService,
	uploadService repository.UploadRepository,
	assetRepository repository.AssetRepository,
) DexPairService {
	path, _ := filepath.Abs("./resource/uniswap_v2_abi.json")
	file, err := os.ReadFile(path)
	if err != nil {
		panic("Failed to read file")
	}
	uniSwapV2Abi, err := abi.JSON(bytes.NewReader(file))
	if err != nil {
		return nil
	}

	return &dexEvmPairService{
		uniSwapV2Abi:    uniSwapV2Abi,
		uploadService:   uploadService,
		gethService:     gethService,
		assetRepository: assetRepository,
		nativeTokenMap:  map[string]*model.Token{},
	}
}
