// protobuf v21 dropped the redundant prefix from the Python generator header.
// Pick whichever this release ships.
#if defined(__has_include) && __has_include(<google/protobuf/compiler/python/generator.h>)
#include <google/protobuf/compiler/python/generator.h>
#else
#include <google/protobuf/compiler/python/python_generator.h>
#endif

#include <google/protobuf/compiler/plugin.h>

int main(int argc, char *argv[]) {
  google::protobuf::compiler::python::Generator generator;
  return google::protobuf::compiler::PluginMain(argc, argv, &generator);
}
