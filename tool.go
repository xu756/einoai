package main

import (
	"context"
	"fmt"
	"log"

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
			log.Printf("Getting weather for %s", input.Location)
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
