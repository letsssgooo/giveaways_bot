//go:build integration

package tests_test

import (
	"os"
	"testing"

	"github.com/letsssgooo/giveaways_bot/tests/utils"
)

func TestMain(m *testing.M) { os.Exit(utils.Run(m)) }
