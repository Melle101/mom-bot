package main

import (
	"fmt"
	"log"
	"math"
	"sort"

	"github.com/BurntSushi/toml"
	govanza "github.com/Melle101/govanza"
	constants "github.com/Melle101/govanza/modules"
)

// Structs
type position struct {
	orderbookID  string
	value        float64
	underlyingID string
}

type settings struct {
	AGG             int    `toml:"AGG"`
	LookbackPeriod  string `toml:"lookbackPeriod"`
	SMAFilterLength int    `toml:"SMAFilterLength"`
	BackupAsset     string `toml:"backupAsset"`
	HoldPeriod      int    `toml:"holdPeriod"`
	HoldPeriodType  string `toml:"holdPeriodType"`
}

type asset struct {
	Asset     string `toml:"asset"`
	AssetID   string `toml:"assetID"`
	TargetLev int    `toml:"targetLev"`
}

type config struct {
	Settings settings `toml:"settings"`
	Universe []asset  `toml:"assets"`
}
type assetInfo struct {
	asset            asset
	percentageChange float64
	relativeSMA      float64
}
type tradeInfo struct {
	sellAsset string
	buyAsset  string
	maxValue  float64
}

func getPercentageChange(assetID string, period string) float32 {
	indexInfo, _ := govanza.GetIndexInfo(assetID)
	lastPrice := float32(indexInfo.PreviousClosingPrice)
	var comparePrice float32

	switch period {
	case "ONE_WEEK":
		comparePrice = float32(indexInfo.HistoricalClosingPrices.OneWeek)
	case "ONE_MONTH":
		comparePrice = float32(indexInfo.HistoricalClosingPrices.OneMonth)
	case "THREE_MONTHS":
		comparePrice = float32(indexInfo.HistoricalClosingPrices.ThreeMonths)
	case "ONE_YEAR":
		comparePrice = float32(indexInfo.HistoricalClosingPrices.OneYear)
	}

	return lastPrice / comparePrice
}

func getRelativeSMA(assetID string, days int) float64 {
	priceData, _ := govanza.GetChartData(assetID)
	sum := 0.0

	for i := range days {
		index := (len(priceData.Ohlc) - 1) - i

		sum += priceData.Ohlc[index].Close
	}

	return priceData.Ohlc[len(priceData.Ohlc)-1].Close / (sum / float64(days))
}

func findUpcomingHoldings(config config) []string {
	assetInfos := make([]assetInfo, len(config.Universe))

	for i, asset := range config.Universe {
		percentageChange := float64(getPercentageChange(asset.AssetID, config.Settings.LookbackPeriod))
		relativeSMA := getRelativeSMA(asset.AssetID, config.Settings.SMAFilterLength)

		assetInfos[i] = assetInfo{
			asset:            asset,
			percentageChange: percentageChange,
			relativeSMA:      relativeSMA,
		}
		fmt.Println(assetInfos[i])
	}

	sort.Slice(assetInfos, func(i, j int) bool {
		return assetInfos[i].percentageChange > assetInfos[j].percentageChange
	})

	upcomingHoldings := make([]string, config.Settings.AGG)

	for i := range config.Settings.AGG {
		if assetInfos[i].relativeSMA > 1 {
			upcomingHoldings[i] = assetInfos[i].asset.AssetID

		} else {
			upcomingHoldings[i] = config.Settings.BackupAsset
		}
	}

	return upcomingHoldings
}

func contains(s []string, e string) bool {
	for _, a := range s {
		if a == e {
			return true
		}
	}
	return false
}

func indexOfAsset(s []asset, e string) int {
	for i, asset := range s {
		if asset.AssetID == e {
			return i
		}
	}
	return -1
}

func indexOfPos(s []position, e string) int {
	for i, pos := range s {
		if pos.orderbookID == e {
			return i
		}
	}
	return -1
}

func findSuitableAsset(underlying string, targetLev int) string {

	searchInfo := constants.WarrantSearch{
		Filter: constants.Filter{
			Directions:            []string{"long"},
			Issuers:               []string{},
			SubTypes:              []string{"mini_future"},
			EndDates:              []string{},
			UnderlyingInstruments: []string{underlying},
		},
		Limit:  20,
		Offset: 0,
		SortBy: constants.SortBy{
			Field: "leverage",
			Order: "asc",
		},
	}

	warrantList, _ := govanza.GetWarrantList(searchInfo)

	sort.Slice(warrantList.Warrants, func(i, j int) bool {
		// Calculate the absolute difference from the target
		distI := math.Abs(float64(warrantList.Warrants[i].Leverage - float64(targetLev)))
		distJ := math.Abs(float64(warrantList.Warrants[j].Leverage - float64(targetLev)))
		return distI < distJ
	})

	for _, warrant := range warrantList.Warrants {
		if warrant.TotalValueTraded > 0 && warrant.Leverage < float64(targetLev)+1 {
			return warrant.OrderbookID
		}
	}

	return warrantList.Warrants[0].OrderbookID

}
func removeDuplicateStr(strSlice []string) []string {
	allKeys := make(map[string]bool)
	list := []string{}
	for _, item := range strSlice {
		if _, value := allKeys[item]; !value {
			allKeys[item] = true
			list = append(list, item)
		}
	}
	return list
}

// There will be a bug if currentPositions < upcomingHoldings. eg. if double in backup asset.
func findTrades(currentPositions []position, upcomingHoldings []string, config config) []tradeInfo {

	var sells []string
	var buys []string

	for _, next := range upcomingHoldings {
		contain := false

		for _, pos := range currentPositions {
			if !contains(upcomingHoldings, pos.underlyingID) {
				sells = append(sells, pos.orderbookID)
			}

			if next == pos.underlyingID {
				contain = true
			}
		}
		if !contain {
			buys = append(buys, next)
		}
	}
	nsells := removeDuplicateStr(sells)

	trades := make([]tradeInfo, len(nsells))

	for i := range nsells {
		assetIndex := indexOfAsset(config.Universe, buys[i])
		buyAsset := findSuitableAsset(buys[i], config.Universe[assetIndex].TargetLev)
		sellIndex := indexOfPos(currentPositions, nsells[i])

		trades[i] = tradeInfo{
			sellAsset: nsells[i],
			buyAsset:  buyAsset,
			maxValue:  currentPositions[sellIndex].value,
		}
	}

	return trades
}

func main() {

	const ACC_URL = "0bEiJzLfZBvIQ1WTELD0ow"

	var config config
	_, err := toml.DecodeFile("./config.toml", &config)
	if err != nil {
		fmt.Println("Error loading TOML file:", err)
	}

	//Get credentials
	var client govanza.Client
	_, err = toml.DecodeFile("./auth.toml", &client)
	if err != nil {
		log.Fatal(err)
	}

	//Init Auth
	client.Auth()

	positions, err := client.GetAccountPositions(ACC_URL)
	if err != nil {
		log.Println(err)
	}

	currentPositions := make([]position, len(positions.AssetPositions))

	for i, pos := range positions.AssetPositions {

		if pos.ID == config.Settings.BackupAsset {
			currentPositions[i].underlyingID = config.Settings.BackupAsset
		} else {
			warrantInfo, _ := govanza.GetWarrantInfo(pos.OrderbookID)
			currentPositions[i].underlyingID = warrantInfo.Underlying.OrderbookID
		}

		currentPositions[i].orderbookID = pos.OrderbookID
		currentPositions[i].value = pos.TotalValue
	}

	upcomingHoldings := findUpcomingHoldings(config)
	fmt.Println("Bör hålla dessa underliggande nästa period.")
	fmt.Println(upcomingHoldings)

	trades := findTrades(currentPositions, upcomingHoldings, config)
	fmt.Println("Gör detta bytet mellan de två Mini_Futures.")
	fmt.Println(trades)

	//client.Disconnect()
}
