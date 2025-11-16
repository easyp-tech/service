package test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"

	plugingeneratorv1 "github.com/easyp-tech/service/api/generator/v1"
)

const (
	serverAddress = "localhost:8080"
	testTimeout   = 60 * time.Second
)

// PluginServiceTestSuite тестовая suite для PluginService
type PluginServiceTestSuite struct {
	suite.Suite
	client plugingeneratorv1.ServiceAPIClient
	conn   *grpc.ClientConn
}

// SetupSuite выполняется один раз перед всеми тестами
func (suite *PluginServiceTestSuite) SetupSuite() {
	conn, err := grpc.NewClient(serverAddress, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(suite.T(), err, "Failed to connect to server")

	suite.conn = conn
	suite.client = plugingeneratorv1.NewServiceAPIClient(conn)
}

// TearDownSuite выполняется один раз после всех тестов
func (suite *PluginServiceTestSuite) TearDownSuite() {
	if suite.conn != nil {
		suite.conn.Close()
	}
}

// TestGenerateCode_PythonProtobuf тестирует генерацию Python protobuf
func (suite *PluginServiceTestSuite) TestGenerateCode_PythonProtobuf() {
	request := suite.createTestRequest("protoc-gen-python:v32.1", "")

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	response, err := suite.client.GenerateCode(ctx, request)
	require.NoError(suite.T(), err, "GenerateCode should not fail")

	// Проверяем статус ответа
	//assert.Equal(suite.T(), "success", response.Status, "Response status should be success")
	//assert.Equal(suite.T(), "Code generation completed successfully", response.CodeGeneratorResponse)

	// Проверяем наличие сгенерированных файлов
	require.NotNil(suite.T(), response.CodeGeneratorResponse, "CodeGeneratorResponse should not be nil")

	files := response.CodeGeneratorResponse.File
	require.NotEmpty(suite.T(), files, "Should generate at least one file")

	// Проверяем первый файл
	file := files[0]
	require.NotNil(suite.T(), file.Name, "Generated file should have a name")
	require.NotNil(suite.T(), file.Content, "Generated file should have content")

	assert.Equal(suite.T(), "test/test_pb2.py", *file.Name, "Generated file name should match expected")
	assert.NotEmpty(suite.T(), *file.Content, "Generated file content should not be empty")
	assert.Greater(suite.T(), len(*file.Content), 1000, "Generated file should have substantial content")

	// Сохраняем файл для проверки
	suite.saveGeneratedFile("python_protobuf", *file.Name, *file.Content)
}

// TestGenerateCode_PythonGRPC тестирует генерацию Python gRPC
func (suite *PluginServiceTestSuite) TestGenerateCode_PythonGRPC() {
	request := suite.createTestRequest("grpc_python:v1.75.0", "")

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	response, err := suite.client.GenerateCode(ctx, request)
	require.NoError(suite.T(), err, "GenerateCode should not fail")

	// Проверяем статус ответа
	//assert.Equal(suite.T(), "success", response.Status, "Response status should be success")
	//assert.Equal(suite.T(), "Code generation completed successfully", response.Message)

	// Проверяем наличие сгенерированных файлов
	require.NotNil(suite.T(), response.CodeGeneratorResponse, "CodeGeneratorResponse should not be nil")

	files := response.CodeGeneratorResponse.File
	require.NotEmpty(suite.T(), files, "Should generate at least one file")

	// Проверяем первый файл
	file := files[0]
	require.NotNil(suite.T(), file.Name, "Generated file should have a name")
	require.NotNil(suite.T(), file.Content, "Generated file should have content")

	assert.Equal(suite.T(), "test/test_pb2_grpc.py", *file.Name, "Generated file name should match expected")
	assert.NotEmpty(suite.T(), *file.Content, "Generated file content should not be empty")
	assert.Greater(suite.T(), len(*file.Content), 2000, "Generated gRPC file should have substantial content")

	// Проверяем, что содержимое содержит gRPC специфичные элементы
	content := *file.Content
	assert.Contains(suite.T(), content, "class TestServiceStub", "Should contain gRPC stub class")
	assert.Contains(suite.T(), content, "class TestServiceServicer", "Should contain gRPC servicer class")
	assert.Contains(suite.T(), content, "add_TestServiceServicer_to_server", "Should contain server registration function")

	// Сохраняем файл для проверки
	suite.saveGeneratedFile("python_grpc", *file.Name, *file.Content)
}

// TestGenerateCode_InvalidPlugin тестирует обработку несуществующего плагина
func (suite *PluginServiceTestSuite) TestGenerateCode_InvalidPlugin() {
	request := suite.createTestRequest("nonexistent-plugin:v1.0.0", "")

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	_, err := suite.client.GenerateCode(ctx, request)
	require.Error(suite.T(), err, "Should return error for nonexistent plugin")

	// Проверяем тип ошибки
	st, ok := status.FromError(err)
	require.True(suite.T(), ok, "Error should be a gRPC status error")
	assert.Equal(suite.T(), codes.Internal, st.Code(), "Should return Internal error code")
	assert.Contains(suite.T(), st.Message(), "failed to run plugin", "Error message should mention plugin execution failure")
}

// TestGenerateCode_EmptyRequest тестирует валидацию пустого запроса
func (suite *PluginServiceTestSuite) TestGenerateCode_EmptyRequest() {
	// Создаем пустой запрос
	request := &plugingeneratorv1.GenerateCodeRequest{
		CodeGeneratorRequest: &pluginpb.CodeGeneratorRequest{},
		PluginName:           "protoc-gen-python:v32.1",
	}

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	_, err := suite.client.GenerateCode(ctx, request)
	require.Error(suite.T(), err, "Should return error for empty request")

	// Проверяем тип ошибки
	st, ok := status.FromError(err)
	require.True(suite.T(), ok, "Error should be a gRPC status error")
	assert.Equal(suite.T(), codes.InvalidArgument, st.Code(), "Should return InvalidArgument error code")
	assert.Contains(suite.T(), st.Message(), "no proto files provided", "Error message should mention missing proto files")
}

// TestGenerateCode_InvalidPluginInfo тестирует валидацию неправильного формата plugin_info
func (suite *PluginServiceTestSuite) TestGenerateCode_InvalidPluginInfo() {
	request := suite.createTestRequest("invalid-plugin-format", "")

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	_, err := suite.client.GenerateCode(ctx, request)
	require.Error(suite.T(), err, "Should return error for invalid plugin info format")

	// Проверяем тип ошибки
	st, ok := status.FromError(err)
	require.True(suite.T(), ok, "Error should be a gRPC status error")
	assert.Equal(suite.T(), codes.InvalidArgument, st.Code(), "Should return InvalidArgument error code")
	assert.Contains(suite.T(), st.Message(), "plugin info must be in format", "Error message should mention format requirement")
}

// TestPluginServiceSuite запускает тестовую suite
func TestPluginServiceSuite(t *testing.T) {
	suite.Run(t, new(PluginServiceTestSuite))
}

// createTestRequest создает тестовый запрос с proto файлом
func (suite *PluginServiceTestSuite) createTestRequest(pluginInfo, parameter string) *plugingeneratorv1.GenerateCodeRequest {
	// Создаем тестовый proto файл
	protoFile := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("test/test.proto"),
		Package: proto.String("test"),
		Syntax:  proto.String("proto3"),
		Options: &descriptorpb.FileOptions{
			GoPackage: proto.String("github.com/easyp-tech/easyp-plugin-server/test;testpb"),
		},
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: proto.String("TestMessage"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{
						Name:   proto.String("name"),
						Number: proto.Int32(1),
						Type:   descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
					},
					{
						Name:   proto.String("value"),
						Number: proto.Int32(2),
						Type:   descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum(),
					},
				},
			},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{
			{
				Name: proto.String("TestService"),
				Method: []*descriptorpb.MethodDescriptorProto{
					{
						Name:       proto.String("GetTest"),
						InputType:  proto.String(".test.TestMessage"),
						OutputType: proto.String(".test.TestMessage"),
					},
				},
			},
		},
	}

	// Создаем CodeGeneratorRequest
	codeGenRequest := &pluginpb.CodeGeneratorRequest{
		FileToGenerate: []string{"test/test.proto"},
		Parameter:      proto.String(parameter),
		ProtoFile:      []*descriptorpb.FileDescriptorProto{protoFile},
	}

	return &plugingeneratorv1.GenerateCodeRequest{
		CodeGeneratorRequest: codeGenRequest,
		PluginName:           pluginInfo,
	}
}

// saveGeneratedFile сохраняет сгенерированный файл для проверки
func (suite *PluginServiceTestSuite) saveGeneratedFile(testName, fileName, content string) {
	outputDir := filepath.Join("generated", testName)
	err := os.MkdirAll(outputDir, 0755)
	if err != nil {
		suite.T().Logf("Warning: failed to create output directory %s: %v", outputDir, err)
		return
	}

	filePath := filepath.Join(outputDir, filepath.Base(fileName))
	err = os.WriteFile(filePath, []byte(content), 0644)
	if err != nil {
		suite.T().Logf("Warning: failed to save file %s: %v", filePath, err)
		return
	}

	suite.T().Logf("Saved generated file: %s", filePath)
}

// BenchmarkPluginService_GenerateCode бенчмарк для измерения производительности
func BenchmarkPluginService_GenerateCode(b *testing.B) {
	// Создаем подключение
	conn, err := grpc.NewClient(serverAddress, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(b, err, "Failed to connect to server")
	defer conn.Close()

	client := plugingeneratorv1.NewServiceAPIClient(conn)

	// Создаем тестовый запрос
	suite := &PluginServiceTestSuite{}
	request := suite.createTestRequest("protoc-gen-python:v32.1", "")

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), testTimeout)

		_, err := client.GenerateCode(ctx, request)
		require.NoError(b, err, "GenerateCode should not fail during benchmark")

		cancel()
	}
}

// Табличные тесты для различных плагинов
func (suite *PluginServiceTestSuite) TestGenerateCode_MultiplePlugins() {
	testCases := []struct {
		name         string
		pluginInfo   string
		expectedFile string
		minSize      int
		contains     []string
	}{
		{
			name:         "Python Protobuf",
			pluginInfo:   "protoc-gen-python:v32.1",
			expectedFile: "test/test_pb2.py",
			minSize:      1000,
			contains:     []string{"_descriptor_pool", "proto3", "TestMessage"},
		},
		{
			name:         "Python gRPC",
			pluginInfo:   "grpc_python:v1.75.0",
			expectedFile: "test/test_pb2_grpc.py",
			minSize:      2000,
			contains:     []string{"TestServiceStub", "TestServiceServicer", "grpc"},
		},
	}

	for _, tc := range testCases {
		suite.Run(tc.name, func() {
			request := suite.createTestRequest(tc.pluginInfo, "")

			ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
			defer cancel()

			response, err := suite.client.GenerateCode(ctx, request)
			require.NoError(suite.T(), err, "GenerateCode should not fail")

			//assert.Equal(suite.T(), "success", response.Status)
			require.NotNil(suite.T(), response.CodeGeneratorResponse)

			files := response.CodeGeneratorResponse.File
			require.NotEmpty(suite.T(), files)

			file := files[0]
			require.NotNil(suite.T(), file.Name)
			require.NotNil(suite.T(), file.Content)

			assert.Equal(suite.T(), tc.expectedFile, *file.Name)
			assert.Greater(suite.T(), len(*file.Content), tc.minSize)

			for _, expectedContent := range tc.contains {
				assert.Contains(suite.T(), *file.Content, expectedContent)
			}
		})
	}
}
