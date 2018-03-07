http_archive(
    name = "io_bazel_rules_go",
    url = "https://github.com/bazelbuild/rules_go/releases/download/0.10.1/rules_go-0.10.1.tar.gz",
    sha256 = "4b14d8dd31c6dbaf3ff871adcd03f28c3274e42abc855cb8fb4d01233c0154dc",
)

http_archive(
    name = "bazel_gazelle",
    url = "https://github.com/bazelbuild/bazel-gazelle/releases/download/0.10.0/bazel-gazelle-0.10.0.tar.gz",
    sha256 = "6228d9618ab9536892aa69082c063207c91e777e51bd3c5544c9c060cafe1bd8",
)

load("@io_bazel_rules_go//go:def.bzl", "go_register_toolchains", "go_repository", "go_rules_dependencies")

go_rules_dependencies()

go_register_toolchains()

load("@bazel_gazelle//:deps.bzl", "gazelle_dependencies")

gazelle_dependencies()

go_repository(
    name = "com_github_abbot_go_http_auth",
    commit = "0ddd408d5d60ea76e320503cc7dd091992dee608",
    importpath = "github.com/abbot/go-http-auth",
)

go_repository(
    name = "com_github_djherbis_atime",
    commit = "8e47e0e01d08df8b9f840d74299c8ab70a024a30",
    importpath = "github.com/djherbis/atime",
)

go_repository(
    name = "org_golang_x_crypto",
    commit = "5119cf507ed5294cc409c092980c7497ee5d6fd2",
    importpath = "golang.org/x/crypto",
)

go_repository(
    name = "org_golang_x_net",
    commit = "f5dfe339be1d06f81b22525fe34671ee7d2c8904",
    importpath = "golang.org/x/net",
)
