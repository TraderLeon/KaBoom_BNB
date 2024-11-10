package eventsync

import (
	"fmt"
	"github.com/cross-space-official/common/businesserror"
	"github.com/cross-space-official/kaboom-service/common"
	"github.com/cross-space-official/kaboom-service/configs"
	"github.com/ethereum/go-ethereum/ethclient"
)

func alchemyURL(apiKey string, chainID string) string {
	return fmt.Sprintf("https://%s.g.alchemy.com/v2/%s", configs.AlchemySupportedChainPathMapping[chainID], apiKey)
}

func infuraURL(apiKey string, chainID string) string {
	return fmt.Sprintf("https://%s.infura.io/v3/%s", configs.InfuraSupportedChainPathMapping[chainID], apiKey)
}

func nodeRealURL(apiKey string, chainID string) string {
	return fmt.Sprintf("https://%s.nodereal.io/v1/%s", configs.NodeRealSupportedChainPathMapping[chainID], apiKey)
}

func bitlayerURL(chainID string) string {
	return fmt.Sprintf("https://%s.bitlayer.org", configs.BitLayerPathMapping[chainID])
}

func morphTestnetURL(prefix, apiKey, chainID string) string {
	return fmt.Sprintf("https://%s.morph-holesky.quiknode.pro/%s", prefix, apiKey)
}

func GetBaseURL(config configs.OnchainClientConfig) string {
	switch config.ChainID {
	case "1", "5", "137", "11155111", "42161", "421614":
		return infuraURL(config.GetInfuraKey(), config.ChainID)
	case "56", "97":
		return nodeRealURL(config.GetNodeRealKey(), config.ChainID)
	case "8453", "84532":
		return alchemyURL(config.GetAlchemyKey(), config.ChainID)
	case "200901", "200810":
		return bitlayerURL(config.ChainID)
	case "2810":
		return morphTestnetURL(config.GetQuickNodePrefix(), config.GetQuickNodeKey(), config.ChainID)
	case "2818":
		return "https://rpc-quicknode.morphl2.io"
	default:
		return ""
	}
}

func NewEthClient(config configs.OnchainClientConfig) (*ethclient.Client, businesserror.XSpaceBusinessError) {
	url := GetBaseURL(config)
	if len(url) == 0 {
		return nil, common.NewRuntimeError(fmt.Errorf("unsupported chain id: %s", config.ChainID))
	}

	client, err := ethclient.Dial(url)
	if err != nil {
		return nil, common.NewRuntimeError(err)
	}

	return client, nil
}
