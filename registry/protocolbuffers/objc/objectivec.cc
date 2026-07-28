// protobuf v21 dropped the redundant prefix from most generator headers, but
// the Objective-C one kept it until v22. Pick whichever this release ships.
#if defined(__has_include) && __has_include(<google/protobuf/compiler/objectivec/generator.h>)
#include <google/protobuf/compiler/objectivec/generator.h>
#else
#include <google/protobuf/compiler/objectivec/objectivec_generator.h>
#endif

#include <google/protobuf/compiler/plugin.h>

int main(int argc, char *argv[]) {
  google::protobuf::compiler::objectivec::ObjectiveCGenerator generator;
  return google::protobuf::compiler::PluginMain(argc, argv, &generator);
}
