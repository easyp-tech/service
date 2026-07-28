// protobuf v21 dropped the redundant prefix from the Java generator header.
// Pick whichever this release ships.
#if defined(__has_include) && __has_include(<google/protobuf/compiler/java/generator.h>)
#include <google/protobuf/compiler/java/generator.h>
#else
#include <google/protobuf/compiler/java/java_generator.h>
#endif

#include <google/protobuf/compiler/plugin.h>

int main(int argc, char *argv[]) {
  google::protobuf::compiler::java::JavaGenerator generator;
  return google::protobuf::compiler::PluginMain(argc, argv, &generator);
}
