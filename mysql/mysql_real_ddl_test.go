package mysql

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// realDumpRoutines is the shape mysqldump actually writes, taken from a MySQL
// 8.4 server. Everything interesting here is wrapped in /*! ... */ blocks,
// which are not comments: MySQL executes what is inside them when the server is
// new enough.
const realDumpRoutines = "DROP TABLE IF EXISTS `users`;\n" +
	"/*!40101 SET @saved_cs_client     = @@character_set_client */;\n" +
	"/*!50503 SET character_set_client = utf8mb4 */;\n" +
	"CREATE TABLE `users` (\n" +
	"  `id` int NOT NULL AUTO_INCREMENT,\n" +
	"  `n` int DEFAULT NULL,\n" +
	"  PRIMARY KEY (`id`)\n" +
	") ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;\n" +
	"/*!40101 SET character_set_client = @saved_cs_client */;\n" +
	"DELIMITER ;;\n" +
	"/*!50003 CREATE*/ /*!50017 DEFINER=`root`@`localhost`*/ /*!50003 TRIGGER `bump` " +
	"BEFORE INSERT ON `users` FOR EACH ROW BEGIN\n" +
	"  SET NEW.n = 1;\n" +
	"  SET NEW.n = NEW.n + 1;\n" +
	"END */;;\n" +
	"DELIMITER ;\n"

// A version block is unwrapped rather than stripped. Deleting it whole deleted
// every routine a real dump contained, along with the view definitions.
func TestMySQL_RealDumpTrigger(t *testing.T) {
	schema, err := NewMySQL().Parse(realDumpRoutines)
	require.NoError(t, err)

	require.Len(t, schema.Tables, 1)
	require.Len(t, schema.Triggers, 1, "a dumped trigger has to be found")

	trigger := schema.Triggers[0]
	assert.Equal(t, "bump", trigger.Name, "the DEFINER clause sits between CREATE and TRIGGER")
	assert.Equal(t, "users", trigger.Table)
	assert.Equal(t, "BEFORE", trigger.Timing)
	assert.Equal(t, "INSERT", trigger.Event)
	assert.Contains(t, trigger.Body, "NEW.n + 1")
}

// The routine has to come back out in a form the server accepts: the body's own
// semicolons protected by a DELIMITER block, FOR EACH ROW present, and the
// BEGIN and END the parser stripped put back.
func TestMySQL_RealDumpTriggerRoundTrip(t *testing.T) {
	schema, err := NewMySQL().Parse(realDumpRoutines)
	require.NoError(t, err)

	out, err := NewMySQL().Generate(schema)
	require.NoError(t, err)

	assert.Contains(t, out, "DELIMITER ;;")
	assert.Contains(t, out, "CREATE TRIGGER bump BEFORE INSERT ON users")
	assert.Contains(t, out, "FOR EACH ROW")
	assert.Contains(t, out, "BEGIN")
	assert.Contains(t, out, "END ;;")

	// The block has to be closed again, or every statement after it is read
	// with the wrong terminator.
	assert.Equal(t, 1, strings.Count(out, "\nDELIMITER ;\n"))
}

// mysqldump writes each view twice: a stand-in of SELECT 1 AS col early on so
// that anything referring to it can be created, then the real definition at the
// end, carrying the attributes the view was created with. The last one wins.
func TestMySQL_RealDumpViewPrefersTheRealDefinition(t *testing.T) {
	const dump = "CREATE TABLE `users` (`id` int NOT NULL, `email` varchar(255), `status` varchar(20));\n" +
		"/*!50001 DROP VIEW IF EXISTS `active_users`*/;\n" +
		"/*!50001 CREATE VIEW `active_users` AS SELECT \n 1 AS `id`,\n 1 AS `email`*/;\n" +
		"/*!50001 DROP VIEW IF EXISTS `active_users`*/;\n" +
		"/*!50001 CREATE ALGORITHM=UNDEFINED */\n" +
		"/*!50013 DEFINER=`root`@`localhost` SQL SECURITY DEFINER */\n" +
		"/*!50001 VIEW `active_users` AS select `users`.`id` AS `id`,`users`.`email` AS `email` " +
		"from `users` where (`users`.`status` = 'active') */;\n"

	schema, err := NewMySQL().Parse(dump)
	require.NoError(t, err)

	require.Len(t, schema.Views, 1, "the stand-in and the real view are one view")
	assert.Contains(t, schema.Views[0].Definition, "from users")
	assert.NotContains(t, schema.Views[0].Definition, "1 AS id",
		"the stand-in must not win over the real definition")
}
