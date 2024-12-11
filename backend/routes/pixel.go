package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"

	"github.com/keep-starknet-strange/art-peace/backend/core"
	routeutils "github.com/keep-starknet-strange/art-peace/backend/routes/utils"
)

func InitPixelRoutes() {
	http.HandleFunc("/get-pixel", getPixel)
	http.HandleFunc("/get-pixel-info", getPixelInfo)
	if !core.ArtPeaceBackend.BackendConfig.Production {
		http.HandleFunc("/place-pixel-devnet", placePixelDevnet)
		http.HandleFunc("/place-extra-pixels-devnet", placeExtraPixelsDevnet)
	}
	http.HandleFunc("/place-pixel-redis", placePixelRedis)
}

func getPixel(w http.ResponseWriter, r *http.Request) {
	positionStr := r.URL.Query().Get("position")
	position, err := strconv.Atoi(positionStr)
	if err != nil {
		routeutils.WriteErrorJson(w, http.StatusBadRequest, "Invalid query position")
		return
	}

	// Check if position is within canvas bounds
	if position < 0 || position >= (int(core.ArtPeaceBackend.CanvasConfig.Canvas.Width)*int(core.ArtPeaceBackend.CanvasConfig.Canvas.Height)) {
		http.Error(w, "Position out of range", http.StatusBadRequest)
		return
	}

	bitfieldType := "u" + strconv.Itoa(int(core.ArtPeaceBackend.CanvasConfig.ColorsBitWidth))
	pos := uint(position) * core.ArtPeaceBackend.CanvasConfig.ColorsBitWidth

	ctx := context.Background()
	val, err := core.ArtPeaceBackend.Databases.Redis.BitField(ctx, "canvas", "GET", bitfieldType, pos).Result()
	if err != nil {
		routeutils.WriteErrorJson(w, http.StatusInternalServerError, "Error getting pixel")
		return
	}

	// TODO: Check this
	pixel := strconv.Itoa(int(val[0]))
	routeutils.WriteDataJson(w, pixel)
}

type PixelInfo struct {
	Address string `json:"address"`
	Name    string `json:"username"`
}

func getPixelInfo(w http.ResponseWriter, r *http.Request) {
	position, err := strconv.Atoi(r.URL.Query().Get("position"))
	if err != nil {
		routeutils.WriteErrorJson(w, http.StatusBadRequest, "Invalid query position")
		return
	}

	queryRes, err := core.PostgresQueryOne[PixelInfo](`
    SELECT p.address, COALESCE(u.name, '') as name FROM Pixels p
    LEFT JOIN Users u ON p.address = u.address WHERE p.position = $1
    ORDER BY p.time DESC LIMIT 1`, position)
	if err != nil {
		routeutils.WriteDataJson(w, "\"0x0000000000000000000000000000000000000000000000000000000000000000\"")
		return
	}

	if queryRes.Name == "" {
		routeutils.WriteDataJson(w, "\"0x"+queryRes.Address+"\"")
	} else {
		routeutils.WriteDataJson(w, "\""+queryRes.Name+"\"")
	}
}

func placePixelDevnet(w http.ResponseWriter, r *http.Request) {
	// Disable this in production
	if routeutils.NonProductionMiddleware(w, r) {
		return
	}

	jsonBody, err := routeutils.ReadJsonBody[map[string]string](r)
	if err != nil {
		routeutils.WriteErrorJson(w, http.StatusBadRequest, "Invalid JSON request body")
		return
	}

	position, err := strconv.Atoi((*jsonBody)["position"])
	if err != nil {
		routeutils.WriteErrorJson(w, http.StatusBadRequest, "Invalid position")
		return
	}

	color, err := strconv.Atoi((*jsonBody)["color"])
	if err != nil {
		routeutils.WriteErrorJson(w, http.StatusBadRequest, "Invalid color")
		return
	}

	timestamp, err := strconv.Atoi((*jsonBody)["timestamp"])
	if err != nil {
		routeutils.WriteErrorJson(w, http.StatusBadRequest, "Invalid time")
		return
	}

	// Validate position range
	if position < 0 || position >= int(core.ArtPeaceBackend.CanvasConfig.Canvas.Width*core.ArtPeaceBackend.CanvasConfig.Canvas.Height) {
		routeutils.WriteErrorJson(w, http.StatusBadRequest, "Position out of range")
		return
	}

	// Validate color format (e.g., validate against allowed colors)
	colorsLength, err := core.PostgresQueryOne[int]("SELECT COUNT(*) FROM colors")
	if err != nil {
		routeutils.WriteErrorJson(w, http.StatusInternalServerError, "Failed to get colors count")
		return
	}
	if color < 0 || color > *colorsLength {
		routeutils.WriteErrorJson(w, http.StatusBadRequest, "Color out of range")
		return
	}

	shellCmd := core.ArtPeaceBackend.BackendConfig.Scripts.PlacePixelDevnet
	contract := os.Getenv("ART_PEACE_CONTRACT_ADDRESS")

	cmd := exec.Command(shellCmd, contract, "place_pixel", strconv.Itoa(position), strconv.Itoa(color), strconv.Itoa(timestamp))
	_, err = cmd.Output()
	if err != nil {
		routeutils.WriteErrorJson(w, http.StatusInternalServerError, "Failed to place pixel on devnet")
		return
	}

	routeutils.WriteResultJson(w, "Pixel placed")
}

type ExtraPixelJson struct {
	ExtraPixels []struct {
		Position int `json:"position"`
		ColorId  int `json:"colorId"`
	} `json:"extraPixels"`
	Timestamp int `json:"timestamp"`
}

func placeExtraPixelsDevnet(w http.ResponseWriter, r *http.Request) {
	// Handle CORS
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Disable this in production
	if routeutils.NonProductionMiddleware(w, r) {
		return
	}

	jsonBody, err := routeutils.ReadJsonBody[ExtraPixelJson](r)
	if err != nil {
		routeutils.WriteErrorJson(w, http.StatusBadRequest, fmt.Sprintf("Invalid JSON request body: %v", err))
		return
	}

	// Validate input
	if len(jsonBody.ExtraPixels) == 0 {
		routeutils.WriteErrorJson(w, http.StatusBadRequest, "No pixels provided")
		return
	}

	shellCmd := core.ArtPeaceBackend.BackendConfig.Scripts.PlaceExtraPixelsDevnet
	contract := os.Getenv("ART_PEACE_CONTRACT_ADDRESS")

	if contract == "" {
		routeutils.WriteErrorJson(w, http.StatusInternalServerError, "Contract address not configured")
		return
	}

	// Format calldata for the contract
	// The contract expects: [pixel_count, pos1, pos2, ..., posN, color1, color2, ..., colorN, timestamp]
	var args []string
	args = append(args, contract, "place_extra_pixels")

	// Add pixel count
	pixelCount := len(jsonBody.ExtraPixels)
	args = append(args, strconv.Itoa(pixelCount))

	// Add positions
	for _, pixel := range jsonBody.ExtraPixels {
		args = append(args, strconv.Itoa(pixel.Position))
	}

	// Add colors
	for _, pixel := range jsonBody.ExtraPixels {
		args = append(args, strconv.Itoa(pixel.ColorId))
	}

	// Add timestamp
	args = append(args, strconv.Itoa(jsonBody.Timestamp))

	// Execute the command
	cmd := exec.Command(shellCmd, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		routeutils.WriteErrorJson(w, http.StatusInternalServerError,
			fmt.Sprintf("Failed to place extra pixels on devnet: %v - Output: %s", err, string(output)))
		return
	}

	// Create response structure
	response := struct {
		Message string `json:"message"`
		Data    struct {
			PixelsPlaced int   `json:"pixelsPlaced"`
			Positions    []int `json:"positions"`
			Colors       []int `json:"colors"`
			Timestamp    int   `json:"timestamp"`
		} `json:"data"`
	}{
		Message: "Extra pixels placed successfully",
		Data: struct {
			PixelsPlaced int   `json:"pixelsPlaced"`
			Positions    []int `json:"positions"`
			Colors       []int `json:"colors"`
			Timestamp    int   `json:"timestamp"`
		}{
			PixelsPlaced: pixelCount,
			Positions:    extractPositions(jsonBody.ExtraPixels),
			Colors:       extractColors(jsonBody.ExtraPixels),
			Timestamp:    jsonBody.Timestamp,
		},
	}

	// Write response
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// Helper functions to extract positions and colors
func extractPositions(pixels []struct {
	Position int `json:"position"`
	ColorId  int `json:"colorId"`
}) []int {
	positions := make([]int, len(pixels))
	for i, pixel := range pixels {
		positions[i] = pixel.Position
	}
	return positions
}

func extractColors(pixels []struct {
	Position int `json:"position"`
	ColorId  int `json:"colorId"`
}) []int {
	colors := make([]int, len(pixels))
	for i, pixel := range pixels {
		colors[i] = pixel.ColorId
	}
	return colors
}

func placePixelRedis(w http.ResponseWriter, r *http.Request) {
	// Only allow admin to place pixels on redis
	if routeutils.AdminMiddleware(w, r) {
		return
	}

	jsonBody, err := routeutils.ReadJsonBody[map[string]uint](r)
	if err != nil {
		routeutils.WriteErrorJson(w, http.StatusBadRequest, "Invalid JSON request body")
		return
	}

	position := (*jsonBody)["position"]
	color := (*jsonBody)["color"]

	canvasWidth := core.ArtPeaceBackend.CanvasConfig.Canvas.Width
	canvasHeight := core.ArtPeaceBackend.CanvasConfig.Canvas.Height

	// Validate position range
	if position >= canvasWidth*canvasHeight {
		routeutils.WriteErrorJson(w, http.StatusBadRequest, "Position out of range")
		return
	}

	// Validate color range (e.g., ensure color value fits within bit width)
	colorsLength, err := core.PostgresQueryOne[uint]("SELECT COUNT(*) FROM colors")
	if err != nil {
		routeutils.WriteErrorJson(w, http.StatusInternalServerError, "Failed to get colors count")
		return
	}

	if color >= *colorsLength {
		routeutils.WriteErrorJson(w, http.StatusBadRequest, "Color out of range")
		return
	}

	bitfieldType := "u" + strconv.Itoa(int(core.ArtPeaceBackend.CanvasConfig.ColorsBitWidth))
	pos := position * core.ArtPeaceBackend.CanvasConfig.ColorsBitWidth

	ctx := context.Background()
	err = core.ArtPeaceBackend.Databases.Redis.BitField(ctx, "canvas", "SET", bitfieldType, pos, color).Err()
	if err != nil {
		routeutils.WriteErrorJson(w, http.StatusInternalServerError, "Error setting pixel on redis")
		return
	}

	routeutils.WriteResultJson(w, "Pixel placed on redis")
}
