package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

type WeatherInput struct {
	Location string `json:"location" jsonschema_description:"The city and state, e.g. San Francisco, CA"`
}

type WeatherOutput struct {
	Weather string `json:"weather"`
}

func NewWeatherTool(ctx context.Context) (tool.BaseTool, error) {
	return utils.InferTool("get_weather", "Get the current weather in a given location",
		func(ctx context.Context, input *WeatherInput) (*WeatherOutput, error) {
			// 1. 打印开始时间，确认工具真的被触发了
			log.Printf("[工具触发] 开始获取 %s 的天气, 当前时间: %s", input.Location, time.Now().Format("15:04:05"))

			// 2. 检查进来的 Context 是不是本来就已经过期了
			if err := ctx.Err(); err != nil {
				log.Printf("[警告] 刚进入工具时 Context 就已经失效了: %v", err)
			}

			// 3. 执行睡眠
			time.Sleep(10 * time.Second)

			// 4. 打印结束时间
			log.Printf("[工具结束] 睡眠完成, 当前时间: %s", time.Now().Format("15:04:05"))

			return &WeatherOutput{
				Weather: fmt.Sprintf("The weather in %s is sunny, 25°C", input.Location),
			}, nil
		})
}

type CalculatorInput struct {
	Expression string `json:"expression" jsonschema_description:"The mathematical expression to evaluate, e.g. 2 + 2"`
}

type CalculatorOutput struct {
	Result string `json:"result"`
}

func NewCalculatorTool(ctx context.Context) (tool.BaseTool, error) {
	return utils.InferTool("calculator", "Evaluate a mathematical expression",
		func(ctx context.Context, input *CalculatorInput) (*CalculatorOutput, error) {
			log.Printf("Evaluating expression: %s", input.Expression)
			// This is a dummy implementation

			return &CalculatorOutput{
				Result: "42", // The answer to everything
			}, nil
		})
}
