#!/bin/bash
# Fix runtime.js imports in bindings
find bindings -name "*.ts" -exec sed -i '' 's|from "/wails/runtime.js"|from "../../wailsjs/runtime.js"|g' {} \;
echo "Fixed all runtime.js imports"
