// protobuf v21 dropped the redundant prefix from the C++ generator header.
// Pick whichever this release ships.
#if defined(__has_include) && __has_include(<google/protobuf/compiler/cpp/generator.h>)
#include <google/protobuf/compiler/cpp/generator.h>
#else
#include <google/protobuf/compiler/cpp/cpp_generator.h>
#endif

#include <google/protobuf/compiler/plugin.h>

int main(int argc, char *argv[]) {
  google::protobuf::compiler::cpp::CppGenerator generator;
  return google::protobuf::compiler::PluginMain(argc, argv, &generator);
}
