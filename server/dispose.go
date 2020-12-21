/**
* Created by GoLand.
* User: link1st
* Date: 2019-08-21
* Time: 15:42
 */

package server

import (
	"fmt"
	"go-stress-testing/model"
	"go-stress-testing/server/client"
	"go-stress-testing/server/golink"
	"go-stress-testing/server/statistics"
	"go-stress-testing/server/verify"
	"math/rand"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	connectionMode = 1 // 1:顺序建立长链接 2:并发建立长链接
)

var (
	parserList = []func(string) ([]funcParser, error){
		// 随机函数替换
		func(input string) ([]funcParser, error) {
			locs, holders, args, err := preParse(`\$RANDOM\((\d+)\s*,\s*(\d+)\s*\)\$`, input, "$1 $2")
			if err != nil {
				return nil, err
			}

			result := make([]funcParser, 0, len(holders))
			for i := 0; i < len(holders); i++ {
				curHolder := holders[i]
				curArg := args[i]
				if len(curHolder) == 0 {
					break
				}
				if len(curArg) < 2 {
					return nil, fmt.Errorf("invalid str:%s args:%v", curHolder, curArg)
				}
				arg1, err := strconv.ParseInt(curArg[0], 10, 0)
				if err != nil {
					return nil, fmt.Errorf("invalid str:%s args:%v err:%w", curHolder, curArg, err)
				}
				arg2, err := strconv.ParseInt(curArg[1], 10, 0)
				if err != nil {
					return nil, fmt.Errorf("invalid str:%s args:%v err:%w", curHolder, curArg, err)
				}
				curP := funcParser{locIndex: locs[i], placeHolder: curHolder}
				curP.parser = func() string {
					result := strconv.FormatInt(rand.Int63n(arg2-arg1)+arg1, 10)
					return result
				}
				result = append(result, curP)
			}

			return result, nil
		},
	}
)

type funcParser struct {
	locIndex    int
	placeHolder string
	parser      func() string
}

func preParse(regStr, input, repl string) (locs []int, hodlers []string, args [][]string, err error) {
	reg, err := regexp.Compile(regStr)
	if err != nil {
		return
	}
	for {
		if len(input) == 0 {
			break
		}
		loc := reg.FindStringIndex(input)
		if len(loc) == 0 {
			return
		}
		ms := input[loc[0]:loc[1]]
		hodlers = append(hodlers, ms)
		tmp := reg.ReplaceAllString(ms, repl)
		args = append(args, strings.Split(tmp, " "))

		input = input[loc[1]:]
		locs = append(locs, loc[0])
	}
	return
}

// 注册验证器
func init() {
	rand.Seed(time.Now().UnixNano())

	// http
	model.RegisterVerifyHttp("statusCode", verify.HttpStatusCode)
	model.RegisterVerifyHttp("json", verify.HttpJson)

	// webSocket
	model.RegisterVerifyWebSocket("json", verify.WebSocketJson)
}

func parseRequest(request *model.Request) (func() *model.Request, error) {
	parsers := make([]funcParser, 0, 0)
	needReplace := len(parserList) > 0
	for _, v := range parserList {
		p, err := v(request.Body)
		if err != nil {
			return nil, err
		}
		parsers = append(parsers, p...)
	}

	return func() *model.Request {
		if !needReplace {
			return request
		}
		body := request.Body
		for _, v := range parsers {
			body = strings.Replace(body, v.placeHolder, v.parser(), 1)
		}
		result := *request
		result.Body = body
		return &result
	}, nil
}

// 处理函数
func Dispose(concurrency, totalNumber uint64, request *model.Request) {

	// 设置接收数据缓存
	ch := make(chan *model.RequestResults, 1000)
	var (
		wg          sync.WaitGroup // 发送数据完成
		wgReceiving sync.WaitGroup // 数据处理完成
	)

	wgReceiving.Add(1)
	go statistics.ReceivingResults(concurrency, ch, &wgReceiving)
	parser, err := parseRequest(request)
	if err != nil {
		panic(err)
	}

	for i := uint64(0); i < concurrency; i++ {
		wg.Add(1)
		// 每个并发连接个性化处理，增加随机支持
		parsedRequest := parser()

		switch request.Form {
		case model.FormTypeHttp:

			go golink.Http(i, ch, totalNumber, &wg, parsedRequest)

		case model.FormTypeWebSocket:

			switch connectionMode {
			case 1:
				// 连接以后再启动协程
				ws := client.NewWebSocket(parsedRequest.Url)
				err := ws.GetConn()
				if err != nil {
					fmt.Println("连接失败:", i, err)

					continue
				}

				go golink.WebSocket(i, ch, totalNumber, &wg, parsedRequest, ws)
			case 2:
				// 并发建立长链接
				go func(i uint64) {
					// 连接以后再启动协程
					ws := client.NewWebSocket(parsedRequest.Url)
					err := ws.GetConn()
					if err != nil {
						fmt.Println("连接失败:", i, err)

						return
					}

					golink.WebSocket(i, ch, totalNumber, &wg, parsedRequest, ws)
				}(i)

				// 注意:时间间隔太短会出现连接失败的报错 默认连接时长:20毫秒(公网连接)
				time.Sleep(5 * time.Millisecond)
			default:

				data := fmt.Sprintf("不支持的类型:%d", connectionMode)
				panic(data)
			}

		default:
			// 类型不支持
			wg.Done()
		}
	}

	// 等待所有的数据都发送完成
	wg.Wait()

	// 延时1毫秒 确保数据都处理完成了
	time.Sleep(1 * time.Millisecond)
	close(ch)

	// 数据全部处理完成了
	wgReceiving.Wait()

	return
}
