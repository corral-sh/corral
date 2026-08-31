# Homebrew formula for Corral.
#
# Canonical copy lives in the tap: https://github.com/corral-sh/homebrew-tap
#     brew tap corral-sh/tap https://github.com/corral-sh/homebrew-tap
#     brew trust corral-sh/tap   # Homebrew >= 6
#     brew install corral-sh/tap/corral
# Keep this file and the tap's Formula/corral.rb identical; bump `tag` on
# every release (the release checklist in CLAUDE.md covers it).
class Corral < Formula
  desc "Run AI coding agents inside isolated Lima VMs on macOS"
  homepage "https://github.com/corral-sh/corral"
  url "https://github.com/corral-sh/corral.git",
      using: :git,
      tag:   "v0.7.0"
  license "Apache-2.0"
  head "https://github.com/corral-sh/corral.git", using: :git, branch: "main"

  depends_on "go" => :build
  depends_on macos: :ventura
  depends_on "lima"

  def install
    version_str = build.head? ? "HEAD-#{Utils.git_short_head}" : version.to_s
    mod = "github.com/corral-sh/corral"
    ldflags = %W[
      -s -w
      -X #{mod}/internal/cli.Version=#{version_str}
      -X #{mod}/internal/cli.Commit=#{Utils.git_short_head}
      -X #{mod}/internal/cli.Date=#{time.iso8601}
    ]
    system "go", "build", *std_go_args(ldflags: ldflags, output: bin/"corral"), "./cmd/corral"
    generate_completions_from_executable(bin/"corral", "completion")
  end

  def caveats
    <<~EOS
      First steps:
        corral setup                 # defaults + prerequisite check
        cd ~/Code/<project> && corral claude
    EOS
  end

  test do
    assert_match "corral", shell_output("#{bin}/corral version")
  end
end
