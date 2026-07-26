#!/bin/bash
cd /Volumes/workspace/project/Ai/TianCan-AI-IDE/frontend
find bindings -name "*.ts" -exec sed -i '' 's|from "/wails/runtime.js"|from "../../wailsjs/runtime.js"|g' {} \;
find bindings -name "*.ts" -exec sed -i '' 's|from "/wailsjs/runtime.js"|from "../../wailsjs/runtime.js"|g' {} \;
echo "Fixed runtime.js imports"
