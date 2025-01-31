package main

import (
	"errors"
	"fmt"
	"log"
	"math"
	"sort"
	"time"

	"github.com/BurntSushi/toml"
	govanza "github.com/Melle101/govanza"
	constants "github.com/Melle101/govanza/modules"
)

// Structs
type position struct {
	orderbookID  string
	value        float64
	underlyingID string
	volume       float64
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
	sellAsset    string
	buyAsset     string
	sellValue    float64
	extraBuyCash float64
	volume       float64
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

func containsStr(s []string, e string) (bool, int) {
	for i, a := range s {
		if a == e {
			return true, i
		}
	}
	return false, -1
}

func indexOfAsset(s []asset, e string) int {
	for i, asset := range s {
		if asset.AssetID == e {
			return i
		}
	}
	return -1
}

func indexOfPosOrderID(s []position, e string) int {

	for i, pos := range s {
		if pos.orderbookID == e {
			return i
		}
	}
	return -1
}
func indexOfPosUnderlyingID(s []position, e string) int {

	for i, pos := range s {
		if pos.underlyingID == e {
			return i
		}
	}
	return -1
}

func findSuitableAsset(underlying string, targetLev int) string {

	searchInfo := constants.WarrantSearch{
		Filter: constants.Filter{
			Directions: []string{"long"},
			Issuers:    []string{},
			SubTypes:   []string{"mini_future"},
			EndDates:   []string{},
		},
		Limit:  20,
		Offset: 0,
		SortBy: constants.SortBy{
			Field: "leverage",
			Order: "asc",
		},
	}

	if underlying == "155458" {
		searchInfo.Filter.NameQuery = "SP500"
	} else {
		searchInfo.Filter.UnderlyingInstruments = []string{underlying}
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

func normalizePositions(positions []position, config config) ([]position, error) {
	if len(positions) == config.Settings.AGG {
		return positions, nil
	}

	backupIndex := -1

	for i, pos := range positions {
		if pos.orderbookID == config.Settings.BackupAsset {
			backupIndex = i
		}
	}

	if backupIndex == -1 {
		return positions, errors.New("Couldn't normalize positions, backup asset not found in positions")
	}

	missingPositions := config.Settings.AGG - len(positions)

	positions[backupIndex].value /= (float64(missingPositions) + 1)

	for range missingPositions {
		positions = append(positions, positions[backupIndex])
	}

	return positions, nil
}

func findTrades(currentPositions []position, upcomingHoldings []string, config config) ([]tradeInfo, error) {

	var trades []tradeInfo

	normalizedPositions, err := normalizePositions(currentPositions, config)

	if err != nil {
		return trades, err
	}

	var sells []string
	var buys []string

	fmt.Println(normalizedPositions)

	for _, next := range upcomingHoldings {

		if indexOfPosUnderlyingID(normalizedPositions, next) == -1 {
			buys = append(buys, next)
		}
	}

	for _, pos := range normalizedPositions {
		contains, index := containsStr(upcomingHoldings, pos.underlyingID)
		if !contains {
			sells = append(sells, pos.orderbookID)
		} else {
			upcomingHoldings[index] = ""
		}
	}

	trades = make([]tradeInfo, len(sells))
	fmt.Println(sells)
	fmt.Println(buys)

	for i := range sells {
		var buyAsset string

		if buys[i] == config.Settings.BackupAsset {
			buyAsset = buys[i]
		} else {
			assetIndex := indexOfAsset(config.Universe, buys[i])
			buyAsset = findSuitableAsset(buys[i], config.Universe[assetIndex].TargetLev)
		}
		sellIndex := indexOfPosOrderID(normalizedPositions, sells[i])

		trades[i] = tradeInfo{
			sellAsset: sells[i],
			buyAsset:  buyAsset,
			sellValue: normalizedPositions[sellIndex].value,
			volume:    normalizedPositions[sellIndex].volume,
		}
	}

	return trades, nil
}

func fundToWarrant(client govanza.Client, accID string, tradeInfo tradeInfo) (string, error) {
	respSell, onAccountDate, err := placeFundOrderWithReTries(client, accID, "SELL", tradeInfo.sellAsset, tradeInfo.sellValue, tradeInfo.volume)

	nextTradeTime := time.Date(onAccountDate.Year(), onAccountDate.Month(), onAccountDate.Day(), 11, 0, 0, 0, time.Local)
	duration := time.Until(nextTradeTime)

	if duration > 0 {
		time.Sleep(duration)
	}

	client.Auth()
	sellStatus, err := client.GetOrderStatus(accID, respSell.OrderID)
	client.Disconnect()

	if sellStatus.Status != "FULLY_EXECUTED" || err != nil {
		return "", errors.New("Problem with executing sell of asset: " + tradeInfo.sellAsset)
	}

	buyValue := tradeInfo.sellValue + tradeInfo.extraBuyCash

	respBuy, err := placeOrderWithReTries(client, accID, "BUY", tradeInfo.buyAsset, buyValue, 0.00)
	if err != nil {
		return "", err
	}

	client.Auth()
	buyStatus, err := client.GetOrderStatus(accID, respSell.OrderID)
	client.Disconnect()

	if buyStatus.Status != "FULLY_EXECUTED" || err != nil {
		return "", errors.New("Problem with executing buy of asset: " + tradeInfo.buyAsset)
	}

	return respBuy.OrderID, nil
}

func warrantToWarrant(client govanza.Client, accID string, tradeInfo tradeInfo) (string, error) {
	respSell, err := placeOrderWithReTries(client, accID, "SELL", tradeInfo.sellAsset, tradeInfo.sellValue, tradeInfo.volume)
	if err != nil {
		return "", err
	}

	client.Auth()
	sellStatus, err := client.GetOrderStatus(accID, respSell.OrderID)
	client.Disconnect()

	if sellStatus.Status != "FULLY_EXECUTED" || err != nil {
		return "", errors.New("Problem with executing sell of asset: " + tradeInfo.sellAsset)
	}

	buyValue := tradeInfo.sellValue + tradeInfo.extraBuyCash

	respBuy, err := placeOrderWithReTries(client, accID, "BUY", tradeInfo.buyAsset, buyValue, 0.00)
	if err != nil {
		return "", err
	}

	client.Auth()
	buyStatus, err := client.GetOrderStatus(accID, respSell.OrderID)
	client.Disconnect()

	if buyStatus.Status != "FULLY_EXECUTED" || err != nil {
		return "", errors.New("Problem with executing buy of asset: " + tradeInfo.buyAsset)
	}

	return respBuy.OrderID, nil

}

func warrantToFund(client govanza.Client, accID string, tradeInfo tradeInfo) (string, error) {
	respSell, err := placeOrderWithReTries(client, accID, "SELL", tradeInfo.sellAsset, tradeInfo.sellValue, tradeInfo.volume)
	if err != nil {
		return "", err
	}

	client.Auth()
	sellStatus, err := client.GetOrderStatus(accID, respSell.OrderID)
	client.Disconnect()

	if sellStatus.Status != "FULLY_EXECUTED" || err != nil {
		return "", errors.New("Problem with executing sell of asset: " + tradeInfo.sellAsset)
	}

	buyValue := tradeInfo.sellValue + tradeInfo.extraBuyCash

	respBuy, onAccountDate, err := placeFundOrderWithReTries(client, accID, "BUY", tradeInfo.buyAsset, buyValue, 0.00)
	if err != nil {
		return "", err
	}

	tradeDoneTime := time.Date(onAccountDate.Year(), onAccountDate.Month(), onAccountDate.Day(), 11, 0, 0, 0, time.Local)
	duration := time.Until(tradeDoneTime)

	if duration > 0 {
		time.Sleep(duration)
	}

	client.Auth()
	buyStatus, err := client.GetOrderStatus(accID, respBuy.OrderID)
	client.Disconnect()

	if buyStatus.Status != "FULLY_EXECUTED" || err != nil {
		return "", errors.New("Problem with executing buy of asset: " + tradeInfo.buyAsset)
	}

	return respBuy.OrderID, nil

}

func placeFundOrderWithReTries(client govanza.Client, accID, side, asset string, value, volume float64) (constants.FundOrderResponse, time.Time, error) {
	client.Auth()

	var onAccountDate time.Time
	var resp constants.FundOrderResponse

	assetInfo, _ := govanza.GetPriceInfo(asset)

	if volume == 0.00 {
		volume = math.Floor(value / assetInfo[0].LastPrice)
	}

	for i := range 10 {
		resp, onAccountDate, _ = client.PlaceFundOrder(accID, asset, side, assetInfo[0].LastPrice, volume)

		if resp.OrderRequestStatus == "SUCCESS" {
			break
		} else if i == 10 {
			return resp, onAccountDate, errors.New("Could not place position. AssetID: " + asset)
		} else {
			time.Sleep(10 * time.Second)
		}
	}

	client.Disconnect()
	return resp, onAccountDate, nil
}

func placeOrderWithReTries(client govanza.Client, accID, side, asset string, value, volume float64) (constants.PlaceOrder, error) {
	client.Auth()

	assetInfo, _ := govanza.GetPriceInfo(asset)

	if volume == 0.00 {
		volume = math.Floor(value / assetInfo[0].LastPrice)
	}

	var resp constants.PlaceOrder

	for i := range 10 {
		resp, _ = client.PlaceOrder(accID, asset, side, int(volume), assetInfo[0].LastPrice)

		if resp.OrderRequestStatus == "SUCCESS" {
			break
		} else if i == 10 {
			return resp, errors.New("Could not place position. AssetID: " + asset)
		} else {
			time.Sleep(10 * time.Second)
		}
	}

	client.Disconnect()

	return resp, nil
}

func makeTrade(client govanza.Client, accID string, tradeInfo tradeInfo, config config) {

	if tradeInfo.sellAsset == config.Settings.BackupAsset {
		fundToWarrant(client, accID, tradeInfo)

	} else if tradeInfo.buyAsset == config.Settings.BackupAsset {
		warrantToFund(client, accID, tradeInfo)
	} else {
		warrantToWarrant(client, accID, tradeInfo)
	}

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

	accounts, _ := client.GetAccountsRAW()
	var accID string

	for _, acc := range accounts {
		if acc.URLParameterID == ACC_URL {
			accID = acc.ID
		}
	}

	client.Disconnect()

	currentPositions := make([]position, len(positions.AssetPositions))

	for i, pos := range positions.AssetPositions {

		if pos.OrderbookID == config.Settings.BackupAsset {
			currentPositions[i] = position{
				underlyingID: config.Settings.BackupAsset,
				orderbookID:  config.Settings.BackupAsset,
				value:        pos.TotalValue,
				volume:       pos.Shares,
			}

		} else {
			warrantInfo, _ := govanza.GetWarrantInfo(pos.OrderbookID)

			currentPositions[i] = position{
				underlyingID: warrantInfo.Underlying.OrderbookID,
				orderbookID:  pos.OrderbookID,
				value:        pos.TotalValue,
				volume:       pos.Shares,
			}
		}
	}

	upcomingHoldings := findUpcomingHoldings(config)
	fmt.Println(currentPositions)
	fmt.Println("Bör hålla dessa underliggande nästa period.")
	fmt.Println(upcomingHoldings)

	trades, err := findTrades(currentPositions, upcomingHoldings, config)

	totalCash := 0.0
	for _, cash := range positions.CashPositions {
		totalCash += cash.TotalValue
	}

	for _, trade := range trades {
		trade.extraBuyCash += (totalCash / float64(len(trades))) * 0.95
	}

	if err != nil {
		fmt.Println(err)
	}

	fmt.Println("Gör detta bytet mellan de två Mini_Futures.")
	fmt.Println(trades)

	for _, trade := range trades {
		go makeTrade(client, accID, trade, config)
	}

	s, _ := client.Disconnect()
	fmt.Println(s)
}
