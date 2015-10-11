package market

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nzai/stockrecorder/config"
	"github.com/nzai/stockrecorder/db"
	"github.com/nzai/stockrecorder/io"
)

type YahooJson struct {
	Chart YahooChart `json:"chart"`
}

type YahooChart struct {
	Result []YahooResult `json:"result"`
	Err    *YahooError   `json:"error"`
}

type YahooError struct {
	Code        string `json:"code"`
	Description string `json:"description"`
}

type YahooResult struct {
	Meta       YahooMeta       `json:"meta"`
	Timestamp  []int64         `json:"timestamp"`
	Indicators YahooIndicators `json:"indicators"`
}

type YahooMeta struct {
	Currency             string              `json:"currency"`
	Symbol               string              `json:"symbol"`
	ExchangeName         string              `json:"exchangeName"`
	InstrumentType       string              `json:"instrumentType"`
	FirstTradeDate       int64               `json:"firstTradeDate"`
	GMTOffset            int                 `json:"gmtoffset"`
	Timezone             string              `json:"timezone"`
	PreviousClose        float32             `json:"previousClose"`
	Scale                int                 `json:"scale"`
	CurrentTradingPeriod YahooTradingPeroid  `json:"currentTradingPeriod"`
	TradingPeriods       YahooTradingPeroids `json:"tradingPeriods"`
	DataGranularity      string              `json:"dataGranularity"`
	ValidRanges          []string            `json:"validRanges"`
}

type YahooTradingPeroid struct {
	Pre     YahooTradingPeroidSection `json:"pre"`
	Regular YahooTradingPeroidSection `json:"regular"`
	Post    YahooTradingPeroidSection `json:"post"`
}

type YahooTradingPeroids struct {
	Pres     [][]YahooTradingPeroidSection `json:"pre"`
	Regulars [][]YahooTradingPeroidSection `json:"regular"`
	Posts    [][]YahooTradingPeroidSection `json:"post"`
}

type YahooTradingPeroidSection struct {
	Timezone  string `json:"timezone"`
	Start     int64  `json:"start"`
	End       int64  `json:"end"`
	GMTOffset int    `json:"gmtoffset"`
}

type YahooIndicators struct {
	Quotes []YahooQuote `json:"quote"`
}

type YahooQuote struct {
	Open   []float32 `json:"open"`
	Close  []float32 `json:"close"`
	High   []float32 `json:"high"`
	Low    []float32 `json:"low"`
	Volume []int64   `json:"volume"`
}

//	从雅虎财经获取上市公司分时数据
func DownloadCompanyDaily(marketName, companyCode, queryCode string, day time.Time) error {

	//	检查数据库是否解析过
	found, err := db.Raw60Exists(marketName, companyCode, day)
	if err != nil {
		return err
	}

	//	解析过的不再重复解析
	if found {
		return nil
	}

	//	如果不存在就抓取
	start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, day.Location())
	end := start.Add(time.Hour * 24)

	pattern := "https://finance-yql.media.yahoo.com/v7/finance/chart/%s?period2=%d&period1=%d&interval=1m&indicators=quote&includeTimestamps=true&includePrePost=true&events=div%7Csplit%7Cearn&corsDomain=finance.yahoo.com"
	url := fmt.Sprintf(pattern, queryCode, end.Unix(), start.Unix())

	//	查询Yahoo财经接口,返回股票分时数据
	content, err := io.DownloadStringRetry(url, retryTimes, retryIntervalSeconds)
	if err != nil {
		return err
	}

	raw := db.Raw60{
		Market:  marketName,
		Code:    companyCode,
		Date:    day,
		Json:    content,
		Status:  0,
		Message: ""}

	//	保存(加入保存队列)
	db.SaveRaw60(raw)

	return nil
}

//	解析雅虎Json
func ParseDailyYahooJson(marketName, companyCode string, date time.Time, buffer []byte) (*db.DailyAnalyzeResult, error) {

	yj := &YahooJson{}
	err := json.Unmarshal(buffer, &yj)
	if err != nil {
		return nil, fmt.Errorf("解析雅虎Json发生错误: %s", err)
	}

	result := &db.DailyAnalyzeResult{
		DailyResult: db.DailyResult{
			Code:    companyCode,
			Market:  marketName,
			Date:    date,
			Error:   false,
			Message: ""},
		Pre:     make([]db.Peroid60, 0),
		Regular: make([]db.Peroid60, 0),
		Post:    make([]db.Peroid60, 0)}

	//	检查数据
	err = validateDailyYahooJson(yj)
	if err != nil {
		result.DailyResult.Error = true
		result.DailyResult.Message = err.Error()

		return result, nil
	}

	periods, quote := yj.Chart.Result[0].Meta.TradingPeriods, yj.Chart.Result[0].Indicators.Quotes[0]
	for index, ts := range yj.Chart.Result[0].Timestamp {

		p := db.Peroid60{
			Code:   companyCode,
			Market: marketName,
			Start:  time.Unix(ts, 0),
			End:    time.Unix(ts+60, 0),
			Open:   quote.Open[index],
			Close:  quote.Close[index],
			High:   quote.High[index],
			Low:    quote.Low[index],
			Volume: quote.Volume[index]}

		//	Pre, Regular, Post
		if ts >= periods.Pres[0][0].Start && ts < periods.Pres[0][0].End {
			result.Pre = append(result.Pre, p)
		} else if ts >= periods.Regulars[0][0].Start && ts < periods.Regulars[0][0].End {
			result.Regular = append(result.Regular, p)
		} else if ts >= periods.Posts[0][0].Start && ts < periods.Posts[0][0].End {
			result.Post = append(result.Regular, p)
		}
	}

	return result, nil
}

//	验证雅虎Json
func validateDailyYahooJson(yj *YahooJson) error {

	if yj.Chart.Err != nil {
		return fmt.Errorf("[%s]%s", yj.Chart.Err.Code, yj.Chart.Err.Description)
	}

	if yj.Chart.Result == nil || len(yj.Chart.Result) == 0 {
		return fmt.Errorf("Result为空")
	}

	if yj.Chart.Result[0].Indicators.Quotes == nil || len(yj.Chart.Result[0].Indicators.Quotes) == 0 {
		return fmt.Errorf("Quotes为空")
	}

	result, quote := yj.Chart.Result[0], yj.Chart.Result[0].Indicators.Quotes[0]
	if len(result.Timestamp) != len(quote.Open) ||
		len(result.Timestamp) != len(quote.Close) ||
		len(result.Timestamp) != len(quote.High) ||
		len(result.Timestamp) != len(quote.Low) ||
		len(result.Timestamp) != len(quote.Volume) {
		return fmt.Errorf("Quotes数量不正确")
	}

	if len(result.Meta.TradingPeriods.Pres) == 0 ||
		len(result.Meta.TradingPeriods.Pres[0]) == 0 ||
		len(result.Meta.TradingPeriods.Posts) == 0 ||
		len(result.Meta.TradingPeriods.Posts[0]) == 0 ||
		len(result.Meta.TradingPeriods.Regulars) == 0 ||
		len(result.Meta.TradingPeriods.Regulars[0]) == 0 {
		return fmt.Errorf("TradingPeriods数量不正确")
	}
	return nil
}

//	保存到文件
func saveDaily(marketName, companyCode string, day time.Time, buffer []byte) error {

	//	文件保存路径
	fileName := fmt.Sprintf("%s_raw.txt", day.Format("20060102"))
	filePath := filepath.Join(config.Get().DataDir, marketName, companyCode, fileName)

	//	不覆盖原文件
	_, err := os.Stat(filePath)
	if os.IsNotExist(err) {
		return io.WriteBytes(filePath, buffer)
	}

	return nil
}
