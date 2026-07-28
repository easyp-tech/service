// The Kotlin generator moved out of the Java compiler directory into its own
// namespace in protobuf v29; before that it lived alongside the Java one.
// Pick whichever header this release actually ships.
#if defined(__has_include) && __has_include(<google/protobuf/compiler/kotlin/generator.h>)
#include <google/protobuf/compiler/kotlin/generator.h>
using KotlinGeneratorImpl = google::protobuf::compiler::kotlin::KotlinGenerator;
#else
#include <google/protobuf/compiler/java/kotlin_generator.h>
using KotlinGeneratorImpl = google::protobuf::compiler::java::KotlinGenerator;
#endif

#include <google/protobuf/compiler/plugin.h>

int main(int argc, char *argv[]) {
  KotlinGeneratorImpl generator;
  return google::protobuf::compiler::PluginMain(argc, argv, &generator);
}
